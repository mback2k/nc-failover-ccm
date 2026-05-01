package nc

import (
	"context"
	"errors"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	nodeHelpers "k8s.io/cloud-provider/node/helpers"
	serviceHelpers "k8s.io/cloud-provider/service/helpers"
	"k8s.io/klog/v2"
)

const (
	serviceNode  = "k8s.mback2k.net/nc-failover-node"
	nodeUserID   = "k8s.mback2k.net/nc-failover-user-id"
	nodeServerID = "k8s.mback2k.net/nc-failover-server-id"
	nodeService  = "nc-failover-service.k8s.mback2k.net/"
)

func (c *cloud) updateServiceNode(service *v1.Service, node *v1.Node) error {
	changes := service.DeepCopy()
	if changes.Annotations == nil {
		changes.Annotations = map[string]string{serviceNode: node.Name}
	} else {
		changes.Annotations[serviceNode] = node.Name
	}
	if changes.Labels == nil {
		changes.Labels = map[string]string{serviceNode: node.Name}
	} else {
		changes.Labels[serviceNode] = node.Name
	}
	_, err := serviceHelpers.PatchService(c.client.CoreV1(), service, changes)
	if err != nil {
		return err
	}
	labelName := nodeService + service.Name
	labels := map[string]string{labelName: "true"}
	if !nodeHelpers.AddOrUpdateLabelsOnNode(c.client, labels, node) {
		return errors.New("failed to update node labels")
	}
	klog.Infof("Added label '%s' to node '%s'", labelName, node.Name)
	return nil
}

func (c *cloud) removeServiceNode(service *v1.Service, clearStatus bool) error {
	nodeName := service.Annotations[serviceNode]
	changes := service.DeepCopy()
	if clearStatus {
		changes.Status.LoadBalancer = v1.LoadBalancerStatus{}
	}
	delete(changes.Annotations, serviceNode)
	delete(changes.Labels, serviceNode)
	_, err := serviceHelpers.PatchService(c.client.CoreV1(), service, changes)
	if err != nil {
		return err
	}
	node, err := c.client.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	labelName := nodeService + service.Name
	labels := map[string]string{labelName: ""}
	if !nodeHelpers.AddOrUpdateLabelsOnNode(c.client, labels, node) {
		return errors.New("failed to update node labels")
	}
	klog.Infof("Removed label '%s' from node '%s'", labelName, node.Name)
	return nil
}
