# Mini Code Runner on k3s

Go API server skeleton for queueing code execution requests and running them as
Kubernetes `Job` resources on k3s.

## API

- `GET /health`
- `GET /system/status`
- `POST /runs`
- `GET /runs`
- `GET /runs/{run_id}`
- `GET /runs/{run_id}/logs`
- `DELETE /runs/{run_id}`

Example:

```bash
curl -X POST http://localhost:8080/runs \
  -H "Content-Type: application/json" \
  -d '{"language":"python","code":"print(\"Hello from k3s\")"}'
```

## Local VM Development

The server first tries in-cluster Kubernetes config. If it is not running in a Pod, it falls back to `KUBECONFIG`, then `~/.kube/config`.

Set `RUNNER_NODE_SELECTOR` to place generated Job Pods on specific nodes:

```bash
RUNNER_NODE_SELECTOR=runner=true ./code-runner-api
```

```bash
go mod tidy
go run ./cmd/api
```

Make sure your Ubuntu VM can access k3s:

```bash
mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
kubectl get nodes
```

Create the namespace before local testing:

```bash
kubectl apply -f deploy/namespace.yaml
```

If you edit the project on Windows and run k3s from the VM, sync the workspace
to the VM before applying manifests:

```powershell
.\scripts\sync-to-vm.ps1 -VmHost <VM_IP>
```

By default, the script copies this project to:

```text
~/mini-code-runner/mini-code-runner
```

## Deploy to k3s

Build or import the image into k3s, then apply the base manifests:

```bash
docker build -t code-runner-api:latest .
docker save code-runner-api:latest -o code-runner-api.tar
sudo k3s ctr images import code-runner-api.tar

kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/redis.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/worker.yaml
kubectl apply -f deploy/service.yaml
kubectl apply -f deploy/ingress.yaml
```

The worker manifest starts at `replicas: 0`. KEDA owns that replica count after
`deploy/keda-scaledobject.yaml` is applied.

After the ingress is applied, open the UI at:

```text
http://code-runner.local/
```

From Windows, add your VM IP and host name to the Windows hosts file so the browser can resolve the ingress host:

```text
<VM_IP> code-runner.local
```

The hosts file is usually at:

```text
C:\Windows\System32\drivers\etc\hosts
```

To rebuild and roll out an updated image:

```bash
go build -o code-runner-api ./cmd/api
docker build -t code-runner-api:latest .
docker save code-runner-api:latest -o code-runner-api.tar
sudo k3s ctr images import code-runner-api.tar
kubectl -n code-runner-system rollout restart deployment/code-runner-api
kubectl -n code-runner-system rollout status deployment/code-runner-api
kubectl -n code-runner-system rollout restart deployment/code-runner-worker
kubectl -n code-runner-system rollout status deployment/code-runner-worker
```

For a single-node learning setup, port-forwarding is often the quickest path:

```bash
kubectl -n code-runner-system port-forward svc/code-runner-api 8080:80
```

Redis is deployed as the internal queue and run metadata store for the planned
Queue + Worker + KEDA flow. Inside the cluster, apps can reach it at:

```text
code-runner-redis.code-runner-system.svc.cluster.local:6379
```

Check Redis after applying the manifest:

```bash
kubectl -n code-runner-system get pods,svc,pvc -l app=code-runner-redis
kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli ping
```

## Current Architecture

```text
Browser UI
  -> code-runner-api Deployment
  -> Redis runs:queue
  -> code-runner-worker Deployment
  -> Kubernetes Job per run
  -> runner Pod on nodes matching RUNNER_NODE_SELECTOR
```

`POST /runs` creates a Redis-backed run record and pushes the `run_id` onto the
Redis queue. Worker pods consume the queue concurrently and create runner Jobs.
KEDA watches the same Redis list length and scales `code-runner-worker`.

The target flow is:

```text
runs:queue length increases
-> KEDA increases code-runner-worker replicas
-> worker Pods consume the queue in parallel
-> Kubernetes Job creation rate increases
```

## What `POST /runs` Creates

For each request, the API creates a Redis-backed run record and pushes the
`run_id` onto the Redis queue.

The API stores:

- `run:{run_id}`: language, code, timeout, status, and creation timestamp.
- `runs:recent`: recent run IDs for UI/API listing.
- `runs:queue`: pending run IDs for future worker pods to consume.

The worker consumes `runs:queue` and creates:

- `ConfigMap`: stores submitted source code as `main.py` or `main.js`.
- `Job`: mounts the ConfigMap at `/workspace` and runs the code in a language image.
- `Pod`: created by the Job with CPU/memory limits and a timeout.

The generated resources are labeled with `runner.example.com/run-id`.

## KEDA Setup

Install KEDA into the k3s cluster:

```bash
kubectl apply --server-side -f https://github.com/kedacore/keda/releases/download/v2.19.0/keda-2.19.0.yaml
kubectl get ns keda
kubectl -n keda rollout status deployment/keda-operator
kubectl -n keda rollout status deployment/keda-metrics-apiserver
```

Apply the Redis queue scaler:

```bash
kubectl apply -f deploy/keda-scaledobject.yaml
kubectl -n code-runner-system get scaledobject
kubectl -n code-runner-system describe scaledobject code-runner-worker-redis-queue
```

The ScaledObject uses:

- target Deployment: `code-runner-worker`
- Redis address: `code-runner-redis.code-runner-system.svc.cluster.local:6379`
- list name: `runs:queue`
- target list length per worker: `1`
- min/max replicas: `0` / `10`

## Test Commands

Verify the base components:

```bash
kubectl -n code-runner-system get deploy,pod,svc
kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli llen runs:queue
kubectl -n code-runner-system logs deploy/code-runner-api --tail=50
kubectl -n code-runner-system logs deploy/code-runner-worker --tail=50
```

Submit a single run:

```bash
curl -X POST http://code-runner.local/runs \
  -H "Content-Type: application/json" \
  -d '{"language":"python","code":"print(\"hello\")","timeout_seconds":30}'
```

Check the status API used by the UI:

```bash
curl http://code-runner.local/system/status
```

Run an autoscaling smoke test from the UI:

1. Set `Batch count` to `20`.
2. Set `Sleep seconds` to `20`.
3. Click `Run`.
4. Watch `queue_depth`, `worker_replicas`, node distribution, and the
   `Live Monitor` timeline.

The UI `Live Monitor` polls `GET /system/status` and shows:

- queued `run_id` values still waiting in Redis `runs:queue`
- active worker pods created by KEDA
- runner Jobs/Pods created for submitted runs
- node placement for runner Pods
- recent queue depth and worker replica history

Run the same test with curl:

```bash
for i in $(seq 1 20); do
  curl -s -X POST http://code-runner.local/runs \
    -H "Content-Type: application/json" \
    -d '{"language":"python","code":"import time\nprint(\"batch run\")\ntime.sleep(20)","timeout_seconds":60}' >/dev/null
done

kubectl -n code-runner-system get deploy code-runner-worker -w
```

After the queue drains and the cooldown period passes, KEDA should return
`code-runner-worker` to `0` replicas:

```bash
kubectl -n code-runner-system get deploy code-runner-worker
kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli llen runs:queue
```

## Multi-node Scheduling

Label a worker node:

```bash
kubectl label node worker1 runner=true
```

The deployment sets `RUNNER_NODE_SELECTOR=runner=true`, so new runner Jobs are scheduled onto nodes with that label.
