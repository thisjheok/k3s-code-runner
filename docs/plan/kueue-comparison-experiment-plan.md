# Kueue 비교 실험 계획서

이 문서는 현재 Mini Code Runner의 `Redis + KEDA + worker` 기반 Job 투입 방식과
`Kueue` 기반 CPU admission control 방식을 비교하기 위한 실험 계획서입니다.

실험의 핵심은 Kueue를 도입했을 때 초과 workload가 Kubernetes scheduler의
`Pending` Pod로 몰리는 대신, Kueue queue에서 quota 기반으로 대기하는지 확인하는
것입니다.

## 실험 목적

다음 두 가지를 검증합니다.

- Kueue 도입 후 runner Pod의 `Pending` 폭증이 기존 Redis + KEDA 방식보다 줄어드는가?
- Kueue가 CPU request와 ClusterQueue quota를 기준으로 동시 실행 Job 수를 제한하는가?

현재 실험은 GPU 도입 전 CPU 기반 사전 검증입니다. 결과가 유의미하면 이후 GPU
`ResourceFlavor`, GPU quota, GPU node autoscaling 실험으로 확장할 수 있습니다.

## 현재 Baseline 구조

현재 실행 흐름:

```text
POST /runs
-> Redis runs:queue
-> KEDA가 code-runner-worker Deployment scale-out
-> worker Pod가 run_id pop
-> worker Pod가 Kubernetes Job 생성
-> runner Pod가 kube-scheduler에 의해 node에 배치
```

기존 Redis + KEDA 부하 실험에서 관찰된 문제:

```text
Queue length = 0
Worker replicas = 0
Runner Pods = Running 36, Pending 74
Worker node CPU = 99-100%
```

즉 Redis queue는 빠르게 비었지만, 실제 실행 workload는 Kubernetes Job/Pod로 넘어가
`Running` 또는 `Pending` 상태로 남았습니다. KEDA는 Redis queue만 보기 때문에, queue가
0이 된 뒤에는 실제 실행 계층의 backlog를 직접 알 수 없습니다.

## Kueue 실험 구조

롤백 가능한 1차 Kueue 실험 흐름:

```text
POST /runs
-> Redis runs:queue
-> KEDA가 code-runner-worker Deployment scale-out
-> worker Pod가 run_id pop
-> worker Pod가 Kueue-managed Kubernetes Job 생성
-> Kueue가 ClusterQueue quota 기준으로 Job admit
-> admit된 Job만 runner Pod 생성
-> admit되지 않은 Job은 Kueue Workload queue에서 대기
```

이 방식은 Redis, KEDA, worker 구조를 유지합니다. 실험을 위해 바꾸는 핵심은
worker가 생성하는 Job에 Kueue queue label을 선택적으로 붙이는 것입니다.

Kueue 효과가 충분히 검증되면, 이후에는 API가 직접 Kueue-managed Job을 생성하고
Redis queue/worker를 Job 투입 경로에서 제거하는 구조를 검토할 수 있습니다.

## 롤백 전략

worker에 feature flag를 추가합니다.

```text
KUEUE_ENABLED=false
KUEUE_QUEUE_NAME=code-runner-queue
```

worker 동작:

```go
if cfg.KueueEnabled {
    job.Labels["kueue.x-k8s.io/queue-name"] = cfg.KueueQueueName
}
```

롤백 명령어:

```bash
kubectl -n code-runner-system set env deployment/code-runner-worker KUEUE_ENABLED=false
kubectl -n code-runner-system rollout restart deployment/code-runner-worker
kubectl -n code-runner-system rollout status deployment/code-runner-worker
```

Kueue 리소스는 남아 있어도 됩니다. 새로 생성되는 Job에
`kueue.x-k8s.io/queue-name` label이 붙지 않으면 Kueue가 해당 Job을 관리하지 않습니다.

실험 중 생성된 Job 정리:

```bash
kubectl -n code-runner-system delete jobs -l app.kubernetes.io/managed-by=mini-code-runner
```

Kueue 테스트 리소스까지 제거해야 할 경우:

```bash
kubectl -n code-runner-system delete localqueue code-runner-queue
kubectl delete clusterqueue code-runner-cq
kubectl delete resourceflavor cpu-default
```

## 적용된 코드 변경

### 1. worker 설정 flag 추가

다음 환경변수를 설정으로 읽을 수 있게 합니다.

```text
KUEUE_ENABLED
KUEUE_QUEUE_NAME
RUNNER_CPU_REQUEST
RUNNER_MEMORY_REQUEST
RUNNER_CPU_LIMIT
RUNNER_MEMORY_LIMIT
```

최소 필수 flag:

- `KUEUE_ENABLED`: generated Job에 Kueue label을 붙일지 결정합니다.
- `KUEUE_QUEUE_NAME`: Job이 제출될 Kueue LocalQueue 이름입니다.
- `RUNNER_CPU_REQUEST`: CPU quota 기반 admission 실험을 위해 runner request를 조정합니다.

주의: Kueue 비교 실험에서 runner CPU request를 `500m`로 올릴 경우,
Redis + KEDA baseline도 반드시 같은 `RUNNER_CPU_REQUEST=500m`,
`RUNNER_CPU_LIMIT=500m` 조건에서 실행해야 합니다. 그래야 Kueue 도입 여부만
실험 변수로 남습니다.

### 2. generated Job에 Kueue label 추가

`KUEUE_ENABLED=true`일 때 worker가 생성하는 Job에 다음 label을 추가합니다.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: code-runner-queue
```

기존 label은 유지합니다.

```yaml
app.kubernetes.io/managed-by: mini-code-runner
runner.example.com/run-id: <run-id>
```

또한 Kueue가 Job을 admission 대상으로 관리할 수 있도록 Job을 처음에는
`suspend: true`로 생성합니다. Kueue가 quota를 확인해 admit하면 Job을 unsuspend하고
그때 runner Pod가 생성됩니다.

```yaml
spec:
  suspend: true
```

`KUEUE_ENABLED=false`일 때는 `suspend: false`로 생성되어 기존 Redis + KEDA 방식처럼
Job이 즉시 실행됩니다.

### 3. runner CPU request 상향

현재 runner 리소스:

```text
requests.cpu = 100m
limits.cpu = 500m
```

Kueue admission 효과를 명확히 보기 위해 실험에서는 다음 값 사용을 권장합니다.

```text
requests.cpu = 500m
limits.cpu = 500m
```

예를 들어 ClusterQueue CPU quota를 `4`로 설정하면 예상 동시 실행 runner Pod 수는:

```text
4 CPU / runner 1개당 0.5 CPU = 8개
```

이렇게 계산할 수 있습니다.

## Kueue 리소스 예시

`deploy/kueue-cpu-queue.yaml` 같은 파일을 생성합니다.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: cpu-default
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: code-runner-cq
spec:
  namespaceSelector: {}
  resourceGroups:
    - coveredResources: ["cpu", "memory"]
      flavors:
        - name: cpu-default
          resources:
            - name: cpu
              nominalQuota: "4"
            - name: memory
              nominalQuota: 4Gi
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: code-runner-system
  name: code-runner-queue
spec:
  clusterQueue: code-runner-cq
```

주의: 설치된 Kueue 버전에 따라 API version이 다를 수 있습니다. 적용 전 확인합니다.

```bash
kubectl api-resources | grep kueue
kubectl explain clusterqueue
```

적용:

```bash
kubectl apply -f deploy/kueue-cpu-queue.yaml
```

확인:

```bash
kubectl get resourceflavor
kubectl get clusterqueue
kubectl -n code-runner-system get localqueue
```

## 실험 입력 조건

두 실험 모두 동일한 workload 패턴을 사용합니다.

```text
초기 workload 수: 80개
추가 workload 수: 30개
추가 workload 투입 시점: Redis queue가 0에 가까워지거나 0이 되었을 때
workload 유형: CPU-bound Python loop
timeout: 180초
```

CPU-bound 요청 payload:

```json
{
  "language": "python",
  "code": "import time\nend=time.time()+120\nx=0\nwhile time.time()<end:\n    x += 1\nprint(x)",
  "timeout_seconds": 180
}
```

## 모니터링 루프

각 실험을 시작하기 전에 VM terminal 1에서 모니터링 루프를 실행합니다.

### Redis + KEDA baseline 모니터링

```bash
while true; do
  echo
  echo "==================== $(date '+%H:%M:%S') ===================="

  echo "[Redis queue length]"
  kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli llen runs:queue

  echo "[Worker deployment]"
  kubectl -n code-runner-system get deploy code-runner-worker --no-headers

  echo "[Runner pod summary]"
  kubectl -n code-runner-system get pods -l app.kubernetes.io/managed-by=mini-code-runner --no-headers 2>/dev/null \
    | awk '{count[$3]++} END {for (s in count) print s, count[s]; if (NR==0) print "none"}'

  echo "[Runner job summary]"
  kubectl -n code-runner-system get jobs -l app.kubernetes.io/managed-by=mini-code-runner --no-headers 2>/dev/null \
    | awk 'END {print "jobs:", NR}'

  echo "[Node usage]"
  kubectl top nodes 2>/dev/null || echo "kubectl top nodes unavailable"

  sleep 2
done | tee keda-baseline-test.log
```

### Kueue 실험 모니터링

```bash
while true; do
  echo
  echo "==================== $(date '+%H:%M:%S') ===================="

  echo "[Redis queue length]"
  kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli llen runs:queue

  echo "[Worker deployment]"
  kubectl -n code-runner-system get deploy code-runner-worker --no-headers

  echo "[Runner pod summary]"
  kubectl -n code-runner-system get pods -l app.kubernetes.io/managed-by=mini-code-runner --no-headers 2>/dev/null \
    | awk '{count[$3]++} END {for (s in count) print s, count[s]; if (NR==0) print "none"}'

  echo "[Kueue workloads]"
  kubectl -n code-runner-system get workloads 2>/dev/null || echo "workloads unavailable"

  echo "[LocalQueue]"
  kubectl -n code-runner-system get localqueue code-runner-queue 2>/dev/null || echo "localqueue unavailable"

  echo "[ClusterQueue]"
  kubectl get clusterqueue code-runner-cq 2>/dev/null || echo "clusterqueue unavailable"

  echo "[Node usage]"
  kubectl top nodes 2>/dev/null || echo "kubectl top nodes unavailable"

  sleep 2
done | tee kueue-admission-test.log
```

종료는 `Ctrl+C`로 합니다. `tee`를 사용하므로 출력은 log 파일에 저장됩니다.

로그 확인:

```bash
less keda-baseline-test.log
less kueue-admission-test.log
```

## Baseline 실험 절차: Redis + KEDA

### 1. Kueue 비활성화

```bash
kubectl -n code-runner-system set env deployment/code-runner-worker KUEUE_ENABLED=false
kubectl -n code-runner-system set env deployment/code-runner-worker RUNNER_CPU_REQUEST=500m
kubectl -n code-runner-system set env deployment/code-runner-worker RUNNER_CPU_LIMIT=500m
kubectl -n code-runner-system rollout restart deployment/code-runner-worker
kubectl -n code-runner-system rollout status deployment/code-runner-worker
```

### 2. 이전 Job 및 queue 정리

```bash
kubectl -n code-runner-system delete jobs -l app.kubernetes.io/managed-by=mini-code-runner
kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli del runs:queue
```

### 3. 모니터링 시작

terminal 1에서 Redis + KEDA baseline 모니터링 루프를 실행합니다.

### 4. workload 80개 투입

terminal 2에서 실행합니다.

```bash
for i in $(seq 1 80); do
  curl -s -X POST http://code-runner.local/runs \
    -H "Content-Type: application/json" \
    -d '{"language":"python","code":"import time\nend=time.time()+120\nx=0\nwhile time.time()<end:\n    x += 1\nprint(x)","timeout_seconds":180}' >/dev/null
done
```

### 5. Redis queue가 0에 가까워졌을 때 workload 30개 추가

terminal 1에서 `[Redis queue length]`가 0에 가까워지거나 0이 되면 terminal 2에서
실행합니다.

```bash
for i in $(seq 1 30); do
  curl -s -X POST http://code-runner.local/runs \
    -H "Content-Type: application/json" \
    -d '{"language":"python","code":"import time\nend=time.time()+120\nx=0\nwhile time.time()<end:\n    x += 1\nprint(x)","timeout_seconds":180}' >/dev/null
done
```

### 6. 모니터링 종료

모든 runner Job이 완료되거나 실패하면 terminal 1에서 `Ctrl+C`로 종료합니다.

## Kueue 실험 절차

### 1. Kueue 리소스 적용

```bash
kubectl apply -f deploy/kueue-cpu-queue.yaml
kubectl get clusterqueue
kubectl -n code-runner-system get localqueue
```

### 2. Kueue mode 활성화

```bash
kubectl -n code-runner-system set env deployment/code-runner-worker KUEUE_ENABLED=true
kubectl -n code-runner-system set env deployment/code-runner-worker KUEUE_QUEUE_NAME=code-runner-queue
kubectl -n code-runner-system set env deployment/code-runner-worker RUNNER_CPU_REQUEST=500m
kubectl -n code-runner-system set env deployment/code-runner-worker RUNNER_CPU_LIMIT=500m
kubectl -n code-runner-system rollout restart deployment/code-runner-worker
kubectl -n code-runner-system rollout status deployment/code-runner-worker
```

환경변수 확인:

```bash
kubectl -n code-runner-system set env deployment/code-runner-worker --list
```

### 3. 이전 Job 및 queue 정리

```bash
kubectl -n code-runner-system delete jobs -l app.kubernetes.io/managed-by=mini-code-runner
kubectl -n code-runner-system exec deploy/code-runner-redis -- redis-cli del runs:queue
```

### 4. 모니터링 시작

terminal 1에서 Kueue 실험 모니터링 루프를 실행합니다.

### 5. workload 80개 투입

terminal 2에서 실행합니다.

```bash
for i in $(seq 1 80); do
  curl -s -X POST http://code-runner.local/runs \
    -H "Content-Type: application/json" \
    -d '{"language":"python","code":"import time\nend=time.time()+120\nx=0\nwhile time.time()<end:\n    x += 1\nprint(x)","timeout_seconds":180}' >/dev/null
done
```

### 6. Redis queue가 0에 가까워졌을 때 workload 30개 추가

terminal 1에서 Redis queue가 0에 가까워지거나 0이 되면 terminal 2에서 실행합니다.

```bash
for i in $(seq 1 30); do
  curl -s -X POST http://code-runner.local/runs \
    -H "Content-Type: application/json" \
    -d '{"language":"python","code":"import time\nend=time.time()+120\nx=0\nwhile time.time()<end:\n    x += 1\nprint(x)","timeout_seconds":180}' >/dev/null
done
```

### 7. 모니터링 종료

모든 runner Job이 완료되거나 실패하면 terminal 1에서 `Ctrl+C`로 종료합니다.

## 예상 결과

### Redis + KEDA baseline 예상 패턴

```text
Redis queue length가 빠르게 0이 됨
worker replica가 증가한 뒤 다시 0으로 감소
runner Pending Pod가 크게 증가
node CPU가 100% 근처까지 상승
backlog가 Kubernetes Pending Pod로 나타남
```

### Kueue 실험 예상 패턴

```text
Redis queue length는 여전히 빠르게 0이 될 수 있음
worker replica도 cooldown 후 0으로 내려갈 수 있음
runner Pending Pod는 baseline보다 줄어야 함
Kueue Workload queue에는 대기 workload가 증가해야 함
Running runner Pod 수는 CPU quota/request 계산값 근처로 제한되어야 함
node CPU 사용률은 더 안정적이어야 함
backlog가 Pending Pod가 아니라 Kueue queued Workload로 나타남
```

중요한 해석:

```text
Kueue queued Workload가 많아지는 것은 실패가 아닙니다.
오히려 Pod scheduling 전에 admission 단계에서 대기시키는 것이므로 의도한 결과입니다.
```

## 결과 지표 및 구하는 방법

두 실험 모두 같은 지표를 수집합니다.

| 지표 | 구하는 방법 | 의미 |
|---|---|---|
| 최대 Redis queue length | monitoring log의 `[Redis queue length]` | Redis 단계 backlog |
| 최대 worker replicas | monitoring log의 `[Worker deployment]` | KEDA worker scale-out 정도 |
| 최대 runner Running Pods | monitoring log의 `[Runner pod summary]` | 동시 실행량 |
| 최대 runner Pending Pods | monitoring log의 `[Runner pod summary]` | 줄이고 싶은 핵심 증상 |
| 최대 Kueue waiting Workloads | `kubectl -n code-runner-system get workloads` | Kueue로 이동한 backlog |
| 최대 node CPU | `kubectl top nodes` | node 포화 여부 |
| 완료 Job 수 | `kubectl get jobs`, pod summary | 처리량 |
| Error/Failed/Timeout Pod 수 | pod summary, `kubectl describe pod` | 실행 안정성 |
| 전체 drain time | 최초 workload 투입 시각부터 전체 완료 시각까지 | 전체 완료 시간 |

수동 확인 명령어:

```bash
kubectl -n code-runner-system get pods -l app.kubernetes.io/managed-by=mini-code-runner
kubectl -n code-runner-system get jobs -l app.kubernetes.io/managed-by=mini-code-runner
kubectl -n code-runner-system get workloads
kubectl -n code-runner-system describe localqueue code-runner-queue
kubectl describe clusterqueue code-runner-cq
kubectl top nodes
kubectl -n code-runner-system get events --sort-by=.lastTimestamp | tail -n 20
```

Pod가 Pending으로 남아 있으면 원인을 확인합니다.

```bash
kubectl -n code-runner-system describe pod <pod-name>
```

Kueue Workload가 admit되지 않으면 원인을 확인합니다.

```bash
kubectl -n code-runner-system describe workload <workload-name>
```

## 비교 결과 표 템플릿

실험 후 아래 표를 채웁니다.

| 지표 | Redis + KEDA baseline | Kueue 실험 | 해석 |
|---|---:|---:|---|
| 초기 workload 수 | 80 | 80 | 동일 입력 |
| 추가 workload 수 | 30 | 30 | 동일 입력 |
| 최대 Redis queue length | TBD | TBD | Redis backlog |
| 최대 worker replicas | TBD | TBD | KEDA scale-out |
| 최대 Running runner Pods | TBD | TBD | 동시 실행량 |
| 최대 Pending runner Pods | TBD | TBD | Kueue에서 감소 기대 |
| 최대 Kueue queued Workloads | N/A | TBD | Kueue admission 대기 |
| 최대 worker node CPU | TBD | TBD | node 포화 여부 |
| Completed Jobs | TBD | TBD | 처리량 |
| Failed/Error/Timeout Jobs | TBD | TBD | 안정성 |
| 전체 drain time | TBD | TBD | 완료 시간 trade-off |

## 성공 기준

Kueue 실험은 다음 조건을 만족하면 성공으로 봅니다.

- runner `Pending` Pod peak가 Redis + KEDA baseline보다 낮다.
- 초과 workload가 Kueue Workload queue에서 대기하는 것이 확인된다.
- Running runner Pod 수가 CPU quota/request 계산값 근처로 제한된다.
- node CPU 포화가 줄거나, 적어도 더 예측 가능한 형태로 나타난다.
- Job 실패/timeout이 baseline보다 크게 증가하지 않는다.

전체 완료 시간은 길어질 수 있습니다. Kueue는 즉시성을 일부 희생하더라도 안정적인
admission과 Pending Pod 폭증 완화를 목표로 하기 때문입니다.

## 주의사항

- Kueue admission은 기본적으로 실제 CPU 사용률이 아니라 resource request와 quota를
  기준으로 동작합니다.
- `RUNNER_CPU_REQUEST`가 너무 낮으면 Kueue가 너무 많은 Job을 admit할 수 있습니다.
  첫 CPU 실험에서는 `500m` 사용을 권장합니다.
- Job에 `kueue.x-k8s.io/queue-name` label이 없으면 Kueue가 관리하지 않습니다.
- 설치된 Kueue CRD API version이 문서의 manifest와 다를 수 있습니다.
  `kubectl api-resources | grep kueue`로 확인해야 합니다.
- 롤백 가능한 실험 구조에서는 Redis queue가 여전히 빠르게 비어질 수 있습니다.
  이때 기대하는 backlog 위치는 Redis queue가 아니라 Kueue Workload queue입니다.
- 전체 완료 시간만 비교하면 Kueue의 장점이 잘 보이지 않습니다. Pending Pod peak,
  node CPU 안정성, 실패/timeout 수를 함께 비교해야 합니다.

## 참고 자료

- Kueue Kubernetes Job guide: https://kueue.sigs.k8s.io/docs/tasks/run/jobs/
- Kueue cluster quota guide: https://kueue.sigs.k8s.io/docs/tasks/manage/administer_cluster_quotas/
- KEDA scaling deployments: https://keda.sh/docs/2.12/concepts/scaling-deployments/
