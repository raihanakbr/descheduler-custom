package actualusageagent

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

func NewCommand() *cobra.Command {
	cfg := Config{
		MetricsSource:           MetricsSourceKubernetesMetrics,
		Interval:                30 * time.Second,
		RunOnce:                 false,
		OutputDir:               "/var/log/actual-usage-agent",
		NodeFragmentationThresh: 0.20,
		Namespace:               metav1.NamespaceAll,
		IncludeControlPlane:     false,
		RemediationMode:         RemediationModeReport,
		EWMABeta:                DefaultEWMABeta,
		PublishTarget:           PublishTargetNone,
	}

	cmd := &cobra.Command{
		Use:   "actual-usage-agent",
		Short: "Observe actual Kubernetes CPU/memory usage and record RII fragmentation history",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.MetricsSource != MetricsSourceKubernetesMetrics {
				return fmt.Errorf("unsupported metrics source %q; currently supported: %s", cfg.MetricsSource, MetricsSourceKubernetesMetrics)
			}
			if cfg.RemediationMode != RemediationModeReport {
				return fmt.Errorf("unsupported remediation mode %q; currently supported: %s", cfg.RemediationMode, RemediationModeReport)
			}
			if cfg.EWMABeta <= 0 || cfg.EWMABeta >= 1 {
				return fmt.Errorf("--ewma-beta must be > 0 and < 1")
			}
			restConfig, err := buildRESTConfig(cfg.Kubeconfig)
			if err != nil {
				return err
			}
			kube, err := kubernetes.NewForConfig(restConfig)
			if err != nil {
				return err
			}
			metrics, err := metricsclientset.NewForConfig(restConfig)
			if err != nil {
				return err
			}
			collector := NewCollector(kube, metrics, cfg)
			return run(cmd.Context(), collector, cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.Kubeconfig, "kubeconfig", cfg.Kubeconfig, "path to kubeconfig; empty uses in-cluster config first, then ~/.kube/config")
	cmd.Flags().StringVar(&cfg.MetricsSource, "metrics-source", cfg.MetricsSource, "metrics source; currently only metrics-server")
	cmd.Flags().DurationVar(&cfg.Interval, "interval", cfg.Interval, "collection interval when --run-once=false")
	cmd.Flags().BoolVar(&cfg.RunOnce, "run-once", cfg.RunOnce, "collect one snapshot then exit")
	cmd.Flags().StringVar(&cfg.OutputDir, "output-dir", cfg.OutputDir, "directory for snapshots.jsonl and node_rii_history.csv")
	cmd.Flags().Float64Var(&cfg.NodeFragmentationThresh, "fragmentation-threshold", cfg.NodeFragmentationThresh, "node is fragmented when abs(RII) is above this value")
	cmd.Flags().StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "namespace to collect pod metrics from; empty means all namespaces")
	cmd.Flags().BoolVar(&cfg.IncludeControlPlane, "include-control-plane", cfg.IncludeControlPlane, "include control-plane/master nodes in RII output")
	cmd.Flags().StringVar(&cfg.RemediationMode, "remediation-mode", cfg.RemediationMode, "remediation behavior; currently report only")
	cmd.Flags().Float64Var(&cfg.EWMABeta, "ewma-beta", cfg.EWMABeta, "EWMA beta for smoothing node CPU/memory usage before RII calculation")
	cmd.Flags().StringVar(&cfg.PublishTarget, "publish-target", cfg.PublishTarget, "where to publish latest usage state: none or node-annotations")
	return cmd
}

func run(ctx context.Context, collector *Collector, cfg Config) error {
	if cfg.RunOnce {
		return collectWriteAndPublish(ctx, collector, cfg)
	}
	if err := collectWriteAndPublish(ctx, collector, cfg); err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := collectWriteAndPublish(ctx, collector, cfg); err != nil {
				return err
			}
		}
	}
}

func collectWriteAndPublish(ctx context.Context, collector *Collector, cfg Config) error {
	snapshot, err := collector.Collect(ctx)
	if err != nil {
		return err
	}
	if err := WriteSnapshot(cfg.OutputDir, snapshot); err != nil {
		return err
	}
	return PublishSnapshot(ctx, collector.kube, cfg.PublishTarget, snapshot)
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		cfg, err := rest.InClusterConfig()
		if err == nil {
			return cfg, nil
		}
		return clientcmd.BuildConfigFromFlags("", defaultKubeconfig())
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func defaultKubeconfig() string {
	if home := homedir.HomeDir(); home != "" {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}
