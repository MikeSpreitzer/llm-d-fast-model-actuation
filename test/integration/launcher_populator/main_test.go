package launcher_populator

import (
	"testing"

	"k8s.io/kubernetes/test/integration/framework"
)

func TestMain(m *testing.M) {
	// klog.InitFlags(flag.CommandLine)
	framework.EtcdMain(m.Run)
}
