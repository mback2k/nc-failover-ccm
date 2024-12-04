package nc

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/cloud-provider/service/helpers"
)

func (c *cloud) updateServiceNode(service *v1.Service, node *v1.Node) (*v1.Service, error) {
	changes := service.DeepCopy()
	changes.Annotations[serviceNode] = node.Name
	changes.Labels[serviceNode] = node.Name
	return helpers.PatchService(c.client.CoreV1(), service, changes)
}

func (c *cloud) removeServiceNode(service *v1.Service, clearStatus bool) (*v1.Service, error) {
	changes := service.DeepCopy()
	delete(changes.Annotations, serviceNode)
	delete(changes.Labels, serviceNode)
	if clearStatus {
		changes.Status.LoadBalancer = v1.LoadBalancerStatus{}
	}
	return helpers.PatchService(c.client.CoreV1(), service, changes)
}
