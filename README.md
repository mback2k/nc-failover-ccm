# netcup Failover Cloud Controller Manager

`nc-failover-ccm` is a Kubernetes Cloud Controller Manager (CCM) for netcup SCP. It treats configured netcup failover IPs as load balancer addresses and manages the corresponding cloud-provider integration.

The controller authenticates against the netcup Server Control Panel using the OAuth 2.0 device flow. After the first login, the resulting token is stored in a Kubernetes Secret and reused by subsequent pod starts.

## Requirements

- Kubernetes cluster with RBAC enabled
- `kubectl` configured for the target cluster
- Helm 3 for the Helm deployment
- A netcup SCP account with access to the failover IPs
- A container runtime or Go 1.27 toolchain when building the image locally

The controller must run with permissions to read and update its ConfigMap and Secret, in addition to the permissions required by the Kubernetes cloud-controller-manager controllers. The included Helm chart creates the required service account and RBAC resources.

## Configuration

The provider configuration is stored in a ConfigMap and referenced by the CCM configuration file:

```yaml
config: nc-failover-ccm-config@kube-system
secret: nc-failover-ccm-secret@kube-system
```

The provider values are YAML documents keyed by their ConfigMap keys. `failoverIPs` contains a list of plain IP addresses. CIDR notation such as `/32` is not supported:

```yaml
- 203.0.113.10
```

The Secret does not need credentials beforehand. On first startup, the controller prints a device-login URL and code to its logs, then stores the obtained OAuth token in the Secret under the `token` key.

Do not commit SCP credentials or generated OAuth tokens. Keep the Kubernetes Secret in the target cluster and restrict access to it with RBAC.

## Helm deployment

Install the chart from the repository checkout:

```sh
kubectl create namespace kube-system --dry-run=client -o yaml | kubectl apply -f -

helm upgrade --install nc-failover-ccm ./charts/ccm \
  --namespace kube-system \
  --set 'ccm.failoverIPs[0]=203.0.113.10'
```

To deploy more than one failover IP, add further values, for example:

```sh
helm upgrade --install nc-failover-ccm ./charts/ccm \
  --namespace kube-system \
  --set 'ccm.failoverIPs[0]=203.0.113.10' \
  --set 'ccm.failoverIPs[1]=203.0.113.11'
```

The default chart configuration deploys two replicas as a `Deployment`. To use one controller pod on every eligible node instead, set `kind=DaemonSet`:

```sh
helm upgrade --install nc-failover-ccm ./charts/ccm \
  --namespace kube-system \
  --set kind=DaemonSet \
  --set 'ccm.failoverIPs[0]=203.0.113.10'
```

Useful chart overrides include:

| Value | Default | Description |
| --- | --- | --- |
| `kind` | `Deployment` | Workload kind; also supports `DaemonSet` |
| `replicaCount` | `2` | Replica count for a `Deployment` |
| `ccm.failoverIPs` | `[]` | Failover IPs managed as load balancers |
| `image.repository` | `ghcr.io/mback2k/nc-failover-ccm` | Container image repository |
| `image.tag` | chart `appVersion` | Container image tag |
| `serviceAccount.create` | `true` | Create the service account, standard CCM and service-controller bindings, and supplemental Service-patcher role and binding |
| `serviceAccount.name` | generated, or `default` when `create=false` | Name of the service account to create or use |

Render and inspect the manifests before applying them:

```sh
helm template nc-failover-ccm ./charts/ccm \
  --namespace kube-system \
  --set 'ccm.failoverIPs[0]=203.0.113.10'
```

Follow the initial device authentication and controller logs with:

```sh
kubectl -n kube-system logs -l app.kubernetes.io/name=nc-failover-ccm -f
```

## Manual deployment

The following is a minimal manual deployment equivalent to the chart. Replace the example IP and image tag before applying it.

### 1. Build and publish the image

Use the published image directly, or build the image locally:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o nc-failover-ccm .
docker build -t ghcr.io/your-org/nc-failover-ccm:dev .
docker push ghcr.io/your-org/nc-failover-ccm:dev
```

When using a local cluster, load the image into its node runtime instead of pushing it, and set `imagePullPolicy: IfNotPresent` in the Deployment.

### 2. Apply provider configuration and RBAC

The application reads provider values from `ConfigMap.data` as plain YAML. The controller still needs access to the Kubernetes API; a standalone container means a manually managed container workload, not a process independent of Kubernetes.

Apply the provider data as a manifest:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nc-failover-ccm-config
  namespace: kube-system
data:
  ccm.yaml: |
    config: nc-failover-ccm-config@kube-system
    secret: nc-failover-ccm-secret@kube-system
  failoverIPs: |
    - 203.0.113.10
---
apiVersion: v1
kind: Secret
metadata:
  name: nc-failover-ccm-secret
  namespace: kube-system
type: Opaque
data: {}
```

Save the manifest as `provider-config.yaml` and apply it:

```sh
kubectl apply -f provider-config.yaml
```

Create the service account and required RBAC bindings and role:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nc-failover-ccm
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nc-failover-ccm
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:cloud-controller-manager
subjects:
  - kind: ServiceAccount
    name: nc-failover-ccm
    namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nc-failover-ccm-service-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:controller:service-controller
subjects:
  - kind: ServiceAccount
    name: nc-failover-ccm
    namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: nc-failover-ccm-service-patcher
rules:
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nc-failover-ccm-service-patcher
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nc-failover-ccm-service-patcher
subjects:
  - kind: ServiceAccount
    name: nc-failover-ccm
    namespace: kube-system
```

Save these RBAC objects as `rbac.yaml` and apply them with `kubectl apply -f rbac.yaml`.

The Helm chart binds its service account to Kubernetes' standard `system:cloud-controller-manager` ClusterRole by default instead of `cluster-admin`. The CCM service controller also needs `system:controller:service-controller`, which grants Service reads and writes to `services/status`; the chart binds this standard role as well. Neither role grants `patch` on Services themselves, which the provider needs to maintain Service labels and annotations. The chart therefore adds a supplemental ClusterRole with only `patch` on Services and binds it to the chart service account. That permission is cluster-wide because the provider reconciles Services in any namespace. The standard CCM role also grants broad cluster-level access, including Secret access; review the role rules on your Kubernetes version and use a reviewed custom role where least privilege is required. To manage a more restrictive RBAC setup yourself, set `serviceAccount.create=false` and provide the name of an existing account with `serviceAccount.name`; otherwise Helm uses the `default` account. Create all required bindings and permissions manually. Verify that the target cluster provides both standard roles and that the combined permissions cover the provider's node, Service, ConfigMap, Secret, event, and leader-election operations.

### 3. Deploy the controller

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nc-failover-ccm
  namespace: kube-system
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nc-failover-ccm
  template:
    metadata:
      labels:
        app: nc-failover-ccm
    spec:
      serviceAccountName: nc-failover-ccm
      priorityClassName: system-cluster-critical
      dnsPolicy: Default
      containers:
        - name: nc-failover-ccm
          image: ghcr.io/mback2k/nc-failover-ccm:v0.3.3
          imagePullPolicy: Always
          args:
            - --cloud-provider=nc
            - --cloud-config=/config/ccm.yaml
            - --leader-elect=true
            - --secure-port=10258
            - --webhook-secure-port=10260
          ports:
            - name: secure
              containerPort: 10258
            - name: webhook-secure
              containerPort: 10260
          volumeMounts:
            - name: config
              mountPath: /config
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: nc-failover-ccm-config
            items:
              - key: ccm.yaml
                path: ccm.yaml
```

Apply the Deployment and authenticate:

```sh
kubectl apply -f deployment.yaml
kubectl -n kube-system logs -l app=nc-failover-ccm -f
```

A `DaemonSet` can be used instead of a `Deployment` when the controller must be scheduled on every eligible node. Keep leader election enabled so only one replica actively reconciles at a time.

## Verification and troubleshooting

```sh
kubectl -n kube-system get pods -l app.kubernetes.io/name=nc-failover-ccm
kubectl -n kube-system get configmap nc-failover-ccm-config -o yaml
kubectl -n kube-system get secret nc-failover-ccm-secret
kubectl -n kube-system describe pod -l app.kubernetes.io/name=nc-failover-ccm
```

Common startup problems:

- `forbidden`: the service account is missing the required ClusterRole or ClusterRoleBinding.
- The device-login message repeats: the token is missing, expired, or cannot be written to the Secret.
- No failover IP is recognized: verify the address format and that the provider values are stored in `ConfigMap.data`.
- The pod cannot start with the local image: load the image into the cluster or change `imagePullPolicy`.

## Development

Run the Go tests and build a Linux binary locally:

```sh
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o nc-failover-ccm .
```

The container image is built from the resulting static binary using the repository `Dockerfile`.

## Standalone container

The image can also be run directly with Docker or another container runtime. It is still a Kubernetes CCM and therefore must be able to reach the Kubernetes API; it is not an independent netcup API client. Use a kubeconfig whose user has the permissions required by the controller, and mount a local cloud configuration file:

```yaml
# /etc/nc-failover-ccm/ccm.yaml
config: nc-failover-ccm-config@kube-system
secret: nc-failover-ccm-secret@kube-system
failoverIPs:
- 203.0.113.10
```

Run the container from a host or external container platform with network access to the cluster API:

```sh
docker run --rm \
  --name nc-failover-ccm \
  -v /etc/nc-failover-ccm:/config:ro \
  -v "$HOME/.kube/config":/kubeconfig:ro \
  -v /etc/ssl/certs:/etc/ssl/certs:ro \
  ghcr.io/mback2k/nc-failover-ccm:v0.3.3 \
  --cloud-provider=nc \
  --cloud-config=/config/ccm.yaml \
  --kubeconfig=/kubeconfig \
  --leader-elect=true \
  --secure-port=10258 \
  --webhook-secure-port=10260
```

The command assumes a Linux host with `/etc/ssl/certs`. The kubeconfig user must be authorized to read and update the referenced ConfigMap and Secret, perform the controller's node and service operations, and participate in leader election. Use only one directly running container unless leader election and its RBAC permissions have been verified for the target cluster. The default `scratch` image has no shell or CA bundle; mount a Linux CA bundle as shown or build a derived image that contains CA certificates.

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
