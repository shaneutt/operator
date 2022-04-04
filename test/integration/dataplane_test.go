//go:build integration_tests
// +build integration_tests

package integration

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kong/gateway-operator/api/v1alpha1"
	"github.com/kong/gateway-operator/controllers"
	"github.com/kong/gateway-operator/internal/consts"
	k8sutils "github.com/kong/gateway-operator/internal/utils/kubernetes"
)

func TestDataplaneEssentials(t *testing.T) {
	t.Log("setting up cleanup")
	cleaner := clusters.NewCleaner(env.Cluster())
	defer func() { assert.NoError(t, cleaner.Cleanup(ctx)) }()

	t.Log("creating a testing namespace")
	namespace, err := k8sClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: uuid.NewString(),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	cleaner.AddNamespace(namespace)

	t.Log("deploying dataplane resource")
	dataplane := &v1alpha1.DataPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace.Name,
			Name:      uuid.NewString(),
		},
	}
	dataplane, err = operatorClient.V1alpha1().DataPlanes(namespace.Name).Create(ctx, dataplane, metav1.CreateOptions{})
	require.NoError(t, err)
	cleaner.Add(dataplane)

	t.Log("verifying dataplane gets marked scheduled")
	require.Eventually(t, func() bool {
		dataplane, err = operatorClient.V1alpha1().DataPlanes(namespace.Name).Get(ctx, dataplane.Name, metav1.GetOptions{})
		require.NoError(t, err)
		isScheduled := false
		for _, condition := range dataplane.Status.Conditions {
			if condition.Type == string(controllers.DataPlaneConditionTypeProvisioned) {
				isScheduled = true
			}
		}
		return isScheduled
	}, time.Minute, time.Second)

	t.Log("verifying that the dataplane gets marked as provisioned")
	require.Eventually(t, func() bool {
		dataplane, err = operatorClient.V1alpha1().DataPlanes(namespace.Name).Get(ctx, dataplane.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		isProvisioned := false
		for _, condition := range dataplane.Status.Conditions {
			if condition.Type == string(controllers.DataPlaneConditionTypeProvisioned) && condition.Status == metav1.ConditionTrue {
				isProvisioned = true
			}
		}
		return isProvisioned
	}, time.Minute*2, time.Second)

	t.Log("verifying deployments managed by the dataplane")
	require.Eventually(t, func() bool {
		deployments, err := k8sutils.ListDeploymentsForOwner(
			ctx,
			mgrClient,
			consts.GatewayOperatorControlledLabel,
			consts.DataPlaneManagedLabelValue,
			dataplane.Namespace,
			dataplane.UID,
		)
		require.NoError(t, err)
		return len(deployments) == 1 && deployments[0].Status.AvailableReplicas >= deployments[0].Status.ReadyReplicas
	}, time.Minute, time.Second)

	t.Log("verifying services managed by the dataplane")
	var dataplaneService *corev1.Service
	require.Eventually(t, func() bool {
		services, err := k8sutils.ListServicesForOwner(
			ctx,
			mgrClient,
			consts.GatewayOperatorControlledLabel,
			consts.DataPlaneManagedLabelValue,
			dataplane.Namespace,
			dataplane.UID,
		)
		require.NoError(t, err)
		if len(services) == 1 {
			dataplaneService = &services[0]
			return true
		}
		return false
	}, time.Minute, time.Second)

	t.Log("verifying dataplane services receive IP addresses")
	var dataplaneIP string
	require.Eventually(t, func() bool {
		dataplaneService, err := k8sClient.CoreV1().Services(dataplane.Namespace).Get(ctx, dataplaneService.Name, metav1.GetOptions{})
		require.NoError(t, err)
		if len(dataplaneService.Status.LoadBalancer.Ingress) > 0 {
			dataplaneIP = dataplaneService.Status.LoadBalancer.Ingress[0].IP
			return true
		}
		return false
	}, time.Minute, time.Second)

	t.Log("verifying connectivity to the dataplane")
	resp, err := httpc.Get(fmt.Sprintf("https://%s:8444/status", dataplaneIP))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"database":{"reachable":true}`)
}
