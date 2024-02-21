package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	k0sconfig "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/replicatedhq/embedded-cluster/pkg/addons"
	"github.com/replicatedhq/embedded-cluster/pkg/config"
	"github.com/replicatedhq/embedded-cluster/pkg/defaults"
	"github.com/replicatedhq/embedded-cluster/pkg/goods"
	"github.com/replicatedhq/embedded-cluster/pkg/helpers"
	"github.com/replicatedhq/embedded-cluster/pkg/metrics"
	"github.com/replicatedhq/embedded-cluster/pkg/preflights"
	"github.com/replicatedhq/embedded-cluster/pkg/prompts"
	"github.com/replicatedhq/embedded-cluster/pkg/release"
	"github.com/replicatedhq/embedded-cluster/pkg/spinner"
)

// ErrNothingElseToAdd is an error returned when there is nothing else to add to the
// screen. This is useful when we want to exit an error from a function here but
// don't want to print anything else (possibly because we have already printed the
// necessary data to the screen).
var ErrNothingElseToAdd = fmt.Errorf("")

// runCommand spawns a command and capture its output. Outputs are logged using the
// logrus package and stdout is returned as a string.
func runCommand(bin string, args ...string) (string, error) {
	fullcmd := append([]string{bin}, args...)
	logrus.Debugf("running command: %v", fullcmd)

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		logrus.Debugf("failed to run command:")
		logrus.Debugf("stdout: %s", stdout.String())
		logrus.Debugf("stderr: %s", stderr.String())
		return "", err
	}
	return stdout.String(), nil
}

// runPostInstall is a helper function that run things just after the k0s install
// command ran.
func runPostInstall() error {
	src := "/etc/systemd/system/k0scontroller.service"
	dst := fmt.Sprintf("/etc/systemd/system/%s.service", defaults.BinaryName())
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}
	if _, err := runCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("unable to get reload systemctl daemon: %w", err)
	}
	return nil
}

// runHostPreflights run the host preflights we found embedded in the binary
// on all configured hosts. We attempt to read HostPreflights from all the
// embedded Helm Charts and from the Kots Application Release files.
func runHostPreflights(c *cli.Context) error {
	hpf, err := addons.NewApplier().HostPreflights()
	if err != nil {
		return fmt.Errorf("unable to read host preflights: %w", err)
	}
	if len(hpf.Collectors) == 0 && len(hpf.Analyzers) == 0 {
		return nil
	}
	pb := spinner.Start()
	pb.Infof("Running host preflights on node")
	output, err := preflights.Run(c.Context, hpf)
	if err != nil {
		pb.CloseWithError()
		return fmt.Errorf("host preflights failed: %w", err)
	}
	if output.HasFail() {
		pb.CloseWithError()
		output.PrintTable()
		return fmt.Errorf("preflights haven't passed on the host")
	}
	if !output.HasWarn() || c.Bool("no-prompt") {
		pb.Close()
		output.PrintTable()
		return nil
	}
	pb.CloseWithError()
	output.PrintTable()
	logrus.Infof("Host preflights have warnings")
	if !prompts.New().Confirm("Do you want to continue ?", false) {
		return fmt.Errorf("user aborted")
	}
	return nil
}

// isAlreadyInstalled checks if the embedded cluster is already installed by looking for
// the k0s configuration file existence.
func isAlreadyInstalled() (bool, error) {
	cfgpath := defaults.PathToK0sConfig()
	_, err := os.Stat(cfgpath)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("unable to check if already installed: %w", err)
	}
}

func checkLicenseMatches(c *cli.Context) error {
	rel, err := release.GetChannelRelease()
	if err != nil {
		return fmt.Errorf("failed to get release from binary: %w", err) // this should only be if the release is malformed
	}

	// handle the three cases that do not require parsing the license file
	// 1. no release and no license, which is OK
	// 2. no license and a release, which is not OK
	// 3. a license and no release, which is not OK
	if rel == nil && c.String("license") == "" {
		// no license and no release, this is OK
		return nil
	} else if rel == nil && c.String("license") != "" {
		// license is present but no release, this means we would install without vendor charts and k0s overrides
		return fmt.Errorf("a license was provided but no release was found in binary, please rerun without the license flag")
	} else if rel != nil && c.String("license") == "" {
		// release is present but no license, this is not OK
		return fmt.Errorf("no license was provided for %s and one is required, please rerun with '--license <path to license file>'", rel.AppSlug)
	}

	license, err := helpers.ParseLicense(c.String("license"))
	if err != nil {
		return fmt.Errorf("unable to parse the license file at %q, please ensure it is not corrupt: %w", c.String("license"), err)
	}

	// Check if the license matches the application version data
	if rel.AppSlug != license.Spec.AppSlug {
		// if the app is different, we will not be able to provide the correct vendor supplied charts and k0s overrides
		return fmt.Errorf("license app %s does not match binary app %s, please provide the correct license", license.Spec.AppSlug, rel.AppSlug)
	}
	if rel.ChannelID != license.Spec.ChannelID {
		// if the channel is different, we will not be able to install the pinned vendor application version within kots
		// this may result in an immediate k8s upgrade after installation, which is undesired
		return fmt.Errorf("license channel %s (%s) does not match binary channel %s, please provide the correct license", license.Spec.ChannelID, license.Spec.ChannelName, rel.ChannelID)
	}

	return nil

}

// createK0sConfig creates a new k0s.yaml configuration file. The file is saved in the
// global location (as returned by defaults.PathToK0sConfig()). If a file already sits
// there, this function returns an error.
func ensureK0sConfig(c *cli.Context, useprompt bool) error {
	cfgpath := defaults.PathToK0sConfig()
	if _, err := os.Stat(cfgpath); err == nil {
		return fmt.Errorf("configuration file already exists")
	}
	if err := os.MkdirAll(filepath.Dir(cfgpath), 0755); err != nil {
		return fmt.Errorf("unable to create directory: %w", err)
	}
	cfg, err := config.RenderK0sConfig(c.Context)
	if err != nil {
		return fmt.Errorf("unable to render config: %w", err)
	}
	opts := []addons.Option{}
	if c.Bool("no-prompt") {
		opts = append(opts, addons.WithoutPrompt())
	}
	if c.String("license") != "" {
		license, err := helpers.ParseLicense(c.String("license"))
		if err != nil {
			return fmt.Errorf("unable to parse license: %w", err)
		}
		opts = append(opts, addons.WithLicense(license))
	}
	if err := config.UpdateHelmConfigs(cfg, opts...); err != nil {
		return fmt.Errorf("unable to update helm configs: %w", err)
	}
	if cfg, err = applyUnsupportedOverrides(c, cfg); err != nil {
		return fmt.Errorf("unable to apply unsupported overrides: %w", err)
	}
	data, err := k8syaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("unable to marshal config: %w", err)
	}
	fp, err := os.OpenFile(cfgpath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("unable to create config file: %w", err)
	}
	defer fp.Close()
	if _, err := fp.Write(data); err != nil {
		return fmt.Errorf("unable to write config file: %w", err)
	}
	return nil
}

// applyUnsupportedOverrides applies overrides to the k0s configuration. Applies first the
// overrides embedded into the binary and after the ones provided by the user (--overrides).
func applyUnsupportedOverrides(c *cli.Context, cfg *k0sconfig.ClusterConfig) (*k0sconfig.ClusterConfig, error) {
	var err error
	if embcfg, err := release.GetEmbeddedClusterConfig(); err != nil {
		return nil, fmt.Errorf("unable to get embedded cluster config: %w", err)
	} else if embcfg != nil {
		overrides := embcfg.Spec.UnsupportedOverrides.K0s
		if cfg, err = config.PatchK0sConfig(cfg, overrides); err != nil {
			return nil, fmt.Errorf("unable to patch k0s config: %w", err)
		}
	}
	if c.String("overrides") == "" {
		return cfg, nil
	}
	eucfg, err := helpers.ParseEndUserConfig(c.String("overrides"))
	if err != nil {
		return nil, fmt.Errorf("unable to process overrides file: %w", err)
	}
	overrides := eucfg.Spec.UnsupportedOverrides.K0s
	if cfg, err = config.PatchK0sConfig(cfg, overrides); err != nil {
		return nil, fmt.Errorf("unable to apply overrides: %w", err)
	}
	return cfg, nil
}

// installK0s runs the k0s install command and waits for it to finish. If no configuration
// is found one is generated.
func installK0s(c *cli.Context) error {
	ourbin := defaults.PathToEmbeddedClusterBinary("k0s")
	hstbin := defaults.K0sBinaryPath()
	if err := os.Rename(ourbin, hstbin); err != nil {
		return fmt.Errorf("unable to move k0s binary: %w", err)
	}
	if _, err := runCommand(hstbin, config.InstallFlags()...); err != nil {
		return fmt.Errorf("unable to install: %w", err)
	}
	if _, err := runCommand(hstbin, "start"); err != nil {
		return fmt.Errorf("unable to start: %w", err)
	}
	return nil
}

// waitForK0s waits for the k0s API to be available. We wait for the k0s socket to
// appear in the system and until the k0s status command to finish.
func waitForK0s(ctx context.Context) error {
	loading := spinner.Start()
	defer loading.Close()
	loading.Infof("Waiting for %s node to be ready", defaults.BinaryName())
	var success bool
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		spath := defaults.PathToK0sStatusSocket()
		if _, err := os.Stat(spath); err != nil {
			continue
		}
		success = true
		break
	}
	if !success {
		return fmt.Errorf("timeout waiting for %s", defaults.BinaryName())
	}
	if _, err := runCommand(defaults.K0sBinaryPath(), "status"); err != nil {
		return fmt.Errorf("unable to get status: %w", err)
	}
	loading.Infof("Node installation finished")
	return nil
}

// runOutro calls Outro() in all enabled addons by means of Applier.
func runOutro(c *cli.Context) error {
	os.Setenv("KUBECONFIG", defaults.PathToKubeConfig())
	opts := []addons.Option{}
	if c.String("license") != "" {
		license, err := helpers.ParseLicense(c.String("license"))
		if err != nil {
			return fmt.Errorf("unable to parse license: %w", err)
		}
		opts = append(opts, addons.WithLicense(license))
	}
	if c.String("overrides") != "" {
		eucfg, err := helpers.ParseEndUserConfig(c.String("overrides"))
		if err != nil {
			return fmt.Errorf("unable to load overrides: %w", err)
		}
		opts = append(opts, addons.WithEndUserConfig(eucfg))
	}
	return addons.NewApplier(opts...).Outro(c.Context)
}

// installCommands executes the "install" command. This will ensure that a k0s.yaml file exists
// and then run `k0s install` to apply the cluster. Once this is finished then a "kubeconfig"
// file is created. Resulting kubeconfig is stored in the configuration dir.
var installCommand = &cli.Command{
	Name:  "install",
	Usage: fmt.Sprintf("Install %s", defaults.BinaryName()),
	Before: func(c *cli.Context) error {
		if os.Getuid() != 0 {
			return fmt.Errorf("install command must be run as root")
		}
		return nil
	},
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "no-prompt",
			Usage: "Do not prompt user when it is not necessary",
			Value: false,
		},
		&cli.StringFlag{
			Name:   "overrides",
			Usage:  "File with an EmbeddedClusterConfig object to override the default configuration",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:   "license",
			Usage:  "Path to the application license file",
			Hidden: false,
		},
	},
	Action: func(c *cli.Context) error {
		logrus.Debugf("checking if %s is already installed", defaults.BinaryName())
		if installed, err := isAlreadyInstalled(); err != nil {
			return err
		} else if installed {
			logrus.Errorf("An installation has been detected on this machine.")
			logrus.Infof("If you want to reinstall you need to remove the existing installation")
			logrus.Infof("first. You can do this by running the following command:")
			logrus.Infof("\n  sudo ./%s node reset\n", defaults.BinaryName())
			return ErrNothingElseToAdd
		}
		metrics.ReportApplyStarted(c)
		logrus.Debugf("checking license matches")
		if err := checkLicenseMatches(c); err != nil {
			metricErr := fmt.Errorf("unable to check license: %w", err)
			metrics.ReportApplyFinished(c, metricErr)
			return err // do not return the metricErr, as we want the user to see the error message without a prefix
		}
		logrus.Debugf("materializing binaries")
		if err := goods.Materialize(); err != nil {
			err := fmt.Errorf("unable to materialize binaries: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("running host preflights")
		if err := runHostPreflights(c); err != nil {
			err := fmt.Errorf("unable to finish preflight checks: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("creating k0s configuration file")
		if err := ensureK0sConfig(c, !c.Bool("no-prompt")); err != nil {
			err := fmt.Errorf("unable to create config file: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("installing k0s")
		if err := installK0s(c); err != nil {
			err := fmt.Errorf("unable update cluster: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("running post install")
		if err := runPostInstall(); err != nil {
			err := fmt.Errorf("unable to run post install: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("waiting for k0s to be ready")
		if err := waitForK0s(c.Context); err != nil {
			err := fmt.Errorf("unable to wait for node: %w", err)
			metrics.ReportApplyFinished(c, err)
			return err
		}
		logrus.Debugf("running outro")
		if err := runOutro(c); err != nil {
			metrics.ReportApplyFinished(c, err)
			return err
		}
		metrics.ReportApplyFinished(c, nil)
		return nil
	},
}
