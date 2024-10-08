package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/replicatedhq/embedded-cluster/pkg/defaults"
	"github.com/replicatedhq/embedded-cluster/pkg/versions"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

// installRunPreflightsCommand runs install host preflights.
var installRunPreflightsCommand = &cli.Command{
	Name:   "run-preflights",
	Hidden: true,
	Usage:  "Run install host preflights",
	Flags: withProxyFlags(withSubnetCIDRFlags(
		[]cli.Flag{
			&cli.StringFlag{
				Name:   "airgap-bundle",
				Usage:  "Path to the air gap bundle. If set, the installation will complete without internet access.",
				Hidden: true,
			},
			&cli.StringFlag{
				Name:    "license",
				Aliases: []string{"l"},
				Usage:   "Path to the license file.",
				Hidden:  false,
			},
			&cli.BoolFlag{
				Name:  "no-prompt",
				Usage: "Disable interactive prompts.",
				Value: false,
			},
			getAdminColsolePortFlag(),
			getLocalArtifactMirrorPortFlag(),
		},
	)),
	Before: func(c *cli.Context) error {
		if os.Getuid() != 0 {
			return fmt.Errorf("run-preflights command must be run as root")
		}
		return nil
	},
	Action: func(c *cli.Context) error {
		var err error
		proxy := getProxySpecFromFlags(c)
		proxy, err = includeLocalIPInNoProxy(c, proxy)
		if err != nil {
			return err
		}
		setProxyEnv(proxy)

		license, err := getLicenseFromFilepath(c.String("license"))
		if err != nil {
			return err
		}

		isAirgap := c.String("airgap-bundle") != ""

		logrus.Debugf("materializing binaries")
		if err := materializeFiles(c); err != nil {
			return err
		}

		applier, err := getAddonsApplier(c, "", proxy)
		if err != nil {
			return err
		}

		logrus.Debugf("running host preflights")
		var replicatedAPIURL, proxyRegistryURL string
		if license != nil {
			replicatedAPIURL = license.Spec.Endpoint
			proxyRegistryURL = fmt.Sprintf("https://%s", defaults.ProxyRegistryAddress)
		}

		adminConsolePort, err := getAdminConsolePortFromFlag(c)
		if err != nil {
			return fmt.Errorf("unable to parse admin console port: %w", err)
		}

		localArtifactMirrorPort, err := getLocalArtifactMirrorPortFromFlag(c)
		if err != nil {
			return fmt.Errorf("unable to parse local artifact mirror port: %w", err)
		}

		if err := RunHostPreflights(c, applier, replicatedAPIURL, proxyRegistryURL, isAirgap, proxy, adminConsolePort, localArtifactMirrorPort); err != nil {
			if err == ErrPreflightsHaveFail {
				return ErrNothingElseToAdd
			}
			return err
		}

		logrus.Info("Host preflights completed successfully")

		return nil
	},
}

// joinRunPreflightsCommand runs install host preflights.
var joinRunPreflightsCommand = &cli.Command{
	Name:      "run-preflights",
	Hidden:    true,
	Usage:     "Run join host preflights",
	ArgsUsage: "<url> <token>",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:   "airgap-bundle",
			Usage:  "Path to the air gap bundle. If set, the installation will complete without internet access.",
			Hidden: true,
		},
		&cli.StringFlag{
			Name:  "network-interface",
			Usage: "The network interface to use for the cluster",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "no-prompt",
			Usage: "Disable interactive prompts.",
			Value: false,
		},
	},
	Before: func(c *cli.Context) error {
		if os.Getuid() != 0 {
			return fmt.Errorf("run-preflights command must be run as root")
		}
		return nil
	},
	Action: func(c *cli.Context) error {
		if c.Args().Len() != 2 {
			return fmt.Errorf("usage: %s join preflights <url> <token>", binName)
		}

		logrus.Debugf("fetching join token remotely")
		jcmd, err := getJoinToken(c.Context, c.Args().Get(0), c.Args().Get(1))
		if err != nil {
			return fmt.Errorf("unable to get join token: %w", err)
		}

		// check to make sure the version returned by the join token is the same as the one we are running
		if jcmd.EmbeddedClusterVersion != versions.Version {
			return fmt.Errorf("embedded cluster version mismatch - this binary is version %q, but the cluster is running version %q", versions.Version, jcmd.EmbeddedClusterVersion)
		}

		setProxyEnv(jcmd.InstallationSpec.Proxy)
		proxyOK, localIP, err := checkProxyConfigForLocalIP(jcmd.InstallationSpec.Proxy, c.String("network-interface"))
		if err != nil {
			return fmt.Errorf("failed to check proxy config for local IP: %w", err)
		}
		if !proxyOK {
			return fmt.Errorf("no-proxy config %q does not allow access to local IP %q", jcmd.InstallationSpec.Proxy.NoProxy, localIP)
		}

		isAirgap := c.String("airgap-bundle") != ""

		logrus.Debugf("materializing binaries")
		if err := materializeFiles(c); err != nil {
			return err
		}

		applier, err := getAddonsApplier(c, "", jcmd.InstallationSpec.Proxy)
		if err != nil {
			return err
		}

		logrus.Debugf("running host preflights")
		replicatedAPIURL := jcmd.InstallationSpec.MetricsBaseURL
		proxyRegistryURL := fmt.Sprintf("https://%s", defaults.ProxyRegistryAddress)

		urlSlices := strings.Split(c.Args().Get(0), ":")
		if len(urlSlices) != 2 {
			return fmt.Errorf("unable to get port from url %s", c.Args().Get(0))
		}
		adminConsolePort, err := strconv.Atoi(urlSlices[1])
		if err != nil {
			return fmt.Errorf("unable to convert port to int: %w", err)
		}

		localArtifactMirrorPort := defaults.LocalArtifactMirrorPort
		if jcmd.InstallationSpec.LocalArtifactMirror != nil {
			localArtifactMirrorPort = jcmd.InstallationSpec.LocalArtifactMirror.Port
		}

		if err := RunHostPreflights(c, applier, replicatedAPIURL, proxyRegistryURL, isAirgap, jcmd.InstallationSpec.Proxy, adminConsolePort, localArtifactMirrorPort); err != nil {
			if err == ErrPreflightsHaveFail {
				return ErrNothingElseToAdd
			}
			return err
		}

		logrus.Info("Host preflights completed successfully")

		return nil
	},
}
