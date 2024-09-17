package config

import (
	_ "embed"
	"fmt"
	"runtime"
	"strings"

	"github.com/k0sproject/k0s/pkg/airgap"
	k0sconfig "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	k0sv1beta1 "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/k0sproject/k0s/pkg/constant"
	"github.com/replicatedhq/embedded-cluster/pkg/release"
	"gopkg.in/yaml.v2"
)

var (
	//go:embed static/metadata.yaml
	rawmetadata []byte
	// Metadata is the unmarshal version of rawmetadata.
	Metadata release.K0sMetadata
)

func init() {
	if err := yaml.Unmarshal(rawmetadata, &Metadata); err != nil {
		panic(fmt.Sprintf("unable to unmarshal metadata: %v", err))
	}
}

func ListK0sImages(cfg *k0sconfig.ClusterConfig) []string {
	var images []string
	for _, image := range airgap.GetImageURIs(cfg.Spec, true) {
		switch image {
		// skip these images
		case cfg.Spec.Images.KubeRouter.CNI.URI(),
			cfg.Spec.Images.KubeRouter.CNIInstaller.URI(),
			cfg.Spec.Images.Konnectivity.URI(),
			cfg.Spec.Network.NodeLocalLoadBalancing.EnvoyProxy.Image.URI():
		default:
			if strings.Contains(image, constant.KubePauseContainerImage) {
				// there's a bug in GetImageURIs where it always returns the original pause image
				// instead of the one in the config, make sure to use the one in the config.
				images = append(images, cfg.Spec.Images.Pause.URI())
			} else {
				images = append(images, image)
			}
		}
	}
	return images
}

func overrideK0sImages(cfg *k0sv1beta1.ClusterConfig) {
	if cfg.Spec.Images == nil {
		cfg.Spec.Images = &k0sv1beta1.ClusterImages{}
	}

	cfg.Spec.Images.CoreDNS.Image = Metadata.Images["coredns"].Repo
	cfg.Spec.Images.CoreDNS.Version = Metadata.Images["coredns"].Tag[runtime.GOARCH]

	cfg.Spec.Images.Calico.Node.Image = Metadata.Images["calico-node"].Repo
	cfg.Spec.Images.Calico.Node.Version = Metadata.Images["calico-node"].Tag[runtime.GOARCH]

	cfg.Spec.Images.Calico.CNI.Image = Metadata.Images["calico-cni"].Repo
	cfg.Spec.Images.Calico.CNI.Version = Metadata.Images["calico-cni"].Tag[runtime.GOARCH]

	cfg.Spec.Images.Calico.KubeControllers.Image = Metadata.Images["calico-kube-controllers"].Repo
	cfg.Spec.Images.Calico.KubeControllers.Version = Metadata.Images["calico-kube-controllers"].Tag[runtime.GOARCH]

	cfg.Spec.Images.MetricsServer.Image = Metadata.Images["metrics-server"].Repo
	cfg.Spec.Images.MetricsServer.Version = Metadata.Images["metrics-server"].Tag[runtime.GOARCH]

	cfg.Spec.Images.KubeProxy.Image = Metadata.Images["kube-proxy"].Repo
	cfg.Spec.Images.KubeProxy.Version = Metadata.Images["kube-proxy"].Tag[runtime.GOARCH]

	cfg.Spec.Images.Pause.Image = Metadata.Images["pause"].Repo
	cfg.Spec.Images.Pause.Version = Metadata.Images["pause"].Tag[runtime.GOARCH]
}
