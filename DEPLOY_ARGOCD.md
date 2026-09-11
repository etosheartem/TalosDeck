# TalosDeck: образ в GitLab Registry и запуск через Argo CD

Инструкция для текущего проекта, 12 сентября 2026. Команды выполняются вручную; Argo CD следит за манифестами в Git. Сборку образов Argo CD не выполняет.

Путь поставки: **исходники → docker build → registry → новый тег в Git → Argo CD sync → Pod**.

## 1. Что нужно заранее

- Работающий Kubernetes/Talos и уже установленный Argo CD в namespace `argocd`.
- На рабочей машине: Docker с Buildx, kubectl, Git, OpenSSL, Argo CD CLI. Команды ниже рассчитаны на Bash: при необходимости сначала запусти `bash`.
- `kubectl` подключён к кластеру, где будет запущена панель. Argo CD зарегистрировал этот же кластер.
- Рабочие `talosconfig` и kubeconfig **управляемого** кластера. В этой инструкции панель разворачивается в нём же.
- StorageClass с provisioner для PVC 20 GiB. Проверить: `kubectl get storageclass`. Если default-класса нет, ниже понадобится явно указать `storageClassName`.
- Из Pod доступны адреса Talos API (`50000`) и Kubernetes API из kubeconfig (обычно `6443`). `127.0.0.1` и адрес локального port-forward в kubeconfig не подходят.
- **Container Registry**: Используется **GitHub Container Registry (GHCR)**: `ghcr.io/etosheartem/talosdeck`. Для публикации нужен GitHub Personal Access Token (PAT) с правами `write:packages`. Если образ сделан публичным в настройках GitHub Package, Kubernetes скачивает его без секретов.

```bash
cd /home/artem/laba-kuber/TalosDeck

git branch --show-current
git status --short
kubectl config current-context
kubectl get nodes -o wide
kubectl get storageclass
kubectl -n argocd get deployments

export TD_REGISTRY='ghcr.io'
export TD_IMAGE='ghcr.io/etosheartem/talosdeck'
export TD_TAG="git-$(git rev-parse --short=12 HEAD)"
export TD_TALOSCONFIG='/home/artem/laba-kuber/cluster-config/talosconfig'
export TD_KUBECONFIG='/home/artem/laba-kuber/kubeconfig'
```

## 2. Собрать и залить образ в GHCR

Для публикации в GHCR авторизуйтесь через `docker login` с вашим GitHub логином и Personal Access Token (classic) с правами `write:packages`:

```bash
read -r -p 'GitHub username: ' TD_PUSH_USER
read -r -s -p 'GitHub Personal Access Token (write:packages): ' TD_PUSH_TOKEN
printf '\n'
printf '%s' "$TD_PUSH_TOKEN" | docker login ghcr.io \
  --username "$TD_PUSH_USER" --password-stdin
unset TD_PUSH_TOKEN

# Сборка под архитектуру x86_64 нод кластера:
docker buildx build --platform linux/amd64 \
  --tag "${TD_IMAGE}:${TD_TAG}" \
  --tag "${TD_IMAGE}:latest" \
  --push .

docker buildx imagetools inspect "${TD_IMAGE}:${TD_TAG}"
```

Dockerfile сам собирает frontend и встраивает его в Go-бинарник; отдельно запускать Vite на сервере не требуется. Архитектуру нод можно посмотреть так:

```bash
kubectl get nodes -L kubernetes.io/arch
```

Для смешанного кластера нужен образ обеих архитектур: `--platform linux/amd64,linux/arm64` и Buildx builder с поддержкой обеих платформ. Не переиспользуй тег для другого содержимого.

Если сборка падает на скачивании базового образа или Go toolchain, проверь доступность версий из Dockerfile и go.mod. Не понижай Go произвольно. В этой работе Docker-сборка и push не выполнялись.

В build context не должно быть локальных секретов: текущий `.dockerignore` не исключает все их возможные имена. Файлы ниже создаются в `/tmp`, а конфиги берутся вне checkout.

## 3. Создать namespace и секреты

Секреты создаём отдельно от Git. Манифесты Application и Deployment содержат только их имена.

```bash
kubectl create namespace talosdeck --dry-run=client -o yaml | kubectl apply -f -

kubectl -n talosdeck create secret generic talosdeck-config \
  --from-file=talosconfig="$TD_TALOSCONFIG" \
  --dry-run=client -o yaml | kubectl apply -f -

# Встроить сертификаты и оставить только выбранный context.
# Подходит kubeconfig с сертификатами/ключом или доступным в Pod токеном;
# exec-плагины рабочей станции в контейнере не установлены.
umask 077
TD_SECRET_DIR=$(mktemp -d)
kubectl --kubeconfig="$TD_KUBECONFIG" config view --raw --minify --flatten \
  > "$TD_SECRET_DIR/kubeconfig"
kubectl -n talosdeck create secret generic talosdeck-kubeconfig \
  --from-file=kubeconfig="$TD_SECRET_DIR/kubeconfig" \
  --dry-run=client -o yaml | kubectl apply -f -

read -r -s -p 'Новый пароль администратора TalosDeck: ' TD_ADMIN_PASSWORD
printf '\n'
# Пароль должен быть непустым и однострочным.
printf 'TALOSDECK_ADMIN_PASSWORD=%s\n' "$TD_ADMIN_PASSWORD" > "$TD_SECRET_DIR/runtime.env"
printf 'TALOSDECK_JWT_SECRET=%s\n' "$(openssl rand -hex 32)" >> "$TD_SECRET_DIR/runtime.env"
unset TD_ADMIN_PASSWORD
kubectl -n talosdeck create secret generic talosdeck-runtime \
  --from-env-file="$TD_SECRET_DIR/runtime.env" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Сохрани введённый пароль в менеджере паролей. При повторной генерации JWT secret существующие сессии станут недействительными после перезапуска приложения.

Теперь pull token. Временный Docker config содержит только учётную запись скачивания, а не все твои Docker credentials:

```bash
read -r -p 'GitLab registry pull username: ' TD_PULL_USER
read -r -s -p 'GitLab registry pull token: ' TD_PULL_TOKEN
printf '\n'
printf '%s' "$TD_PULL_TOKEN" | docker --config "$TD_SECRET_DIR/docker" \
  login "$TD_REGISTRY" --username "$TD_PULL_USER" --password-stdin
unset TD_PULL_TOKEN
kubectl -n talosdeck create secret generic talosdeck-registry \
  --type=kubernetes.io/dockerconfigjson \
  --from-file=.dockerconfigjson="$TD_SECRET_DIR/docker/config.json" \
  --dry-run=client -o yaml | kubectl apply -f -

rm -rf -- "$TD_SECRET_DIR"
unset TD_SECRET_DIR
kubectl -n talosdeck get secrets
```

Явный `KUBECONFIG` ниже нужен потому, что без него backend выберет in-cluster ServiceAccount, а базовый Deployment не имеет необходимых RBAC-разрешений. Используй учётные данные предназначенного для управления кластера: они определяют права всех Kubernetes-операций панели. Административный kubeconfig даёт широкие права; для ограниченной установки нужна отдельная учётная запись и проверенный набор RBAC под используемые функции.

## 4. Подготовить каталог GitOps

Создаём самостоятельный каталог с явным списком ресурсов. Argo CD будет читать только его. Namespace и секреты уже созданы; они не включены в приложение.

```bash
mkdir -p gitops/talosdeck
cp deploy/deployment.yaml deploy/service.yaml deploy/pvc.yaml gitops/talosdeck/

cat > gitops/talosdeck/kustomization.yaml <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: talosdeck
resources:
  - deployment.yaml
  - service.yaml
  - pvc.yaml
patches:
  - target:
      kind: Deployment
      name: talosdeck
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: talosdeck
      spec:
        strategy:
          type: Recreate
        template:
          spec:
            automountServiceAccountToken: false
            imagePullSecrets:
              - name: talosdeck-registry
            containers:
              - name: talosdeck
                envFrom:
                  - secretRef:
                      name: talosdeck-runtime
                env:
                  - name: KUBECONFIG
                    value: /etc/kubernetes/kubeconfig
                volumeMounts:
                  - name: kubeconfig-vol
                    mountPath: /etc/kubernetes
                    readOnly: true
            volumes:
              - name: kubeconfig-vol
                secret:
                  secretName: talosdeck-kubeconfig
  - target:
      kind: Service
      name: talosdeck
    patch: |-
      - op: replace
        path: /spec/type
        value: ClusterIP
      - op: remove
        path: /spec/ports/0/nodePort
  - target:
      kind: PersistentVolumeClaim
      name: talosdeck-data
    patch: |-
      apiVersion: v1
      kind: PersistentVolumeClaim
      metadata:
        name: talosdeck-data
        annotations:
          argocd.argoproj.io/sync-options: Prune=false,Delete=false
YAML

cat >> gitops/talosdeck/kustomization.yaml <<YAML
images:
  - name: talosdeck
    newName: ${TD_IMAGE}
    newTag: "${TD_TAG}"
YAML

kubectl kustomize gitops/talosdeck > /tmp/talosdeck-rendered.yaml
```

Если нет default StorageClass, в `gitops/talosdeck/pvc.yaml` добавь под `spec` строку `storageClassName: ИМЯ_ТВОЕГО_КЛАССА`. Для существующего PVC смена класса требует отдельного переноса данных.

`Recreate` предотвращает одновременную работу старого и нового Pod с одним каталогом данных; обновление будет с коротким простоем. PVC хранит аудит и резервные копии в `/app/data`. Аннотации сохраняют PVC при prune/удалении через Argo CD; прямое удаление namespace или PVC этим не предотвращается.

Service здесь `ClusterIP`: первый доступ через port-forward. Для постоянного адреса подключи существующий Ingress/Gateway с TLS, backend `talosdeck:8080` и поддержкой WebSocket. Если нужен исходный NodePort `32000` в LAN, удали из kustomization только патч Service; это откроет HTTP на адресах нод.

```bash
git add gitops/talosdeck
git commit -m "deploy: add TalosDeck Argo CD manifests"
git push gitlab HEAD
```

Каталог — копия базовых манифестов: дальнейшие изменения поставки вноси в `gitops/talosdeck`, иначе Argo CD их не увидит. [Как Argo CD обрабатывает Kustomize](https://argo-cd.readthedocs.io/en/stable/user-guide/kustomize/).

## 5. Подключить GitLab к Argo CD

Доступ к Git и скачивание образа — отдельные учётные данные. `imagePullSecrets` не дают Argo CD доступ к Git.

В интерфейсе Argo CD: **Settings → Repositories → Connect Repo**:

- Type: `git`.
- Repository URL: `git@gitlab.lan:root/talosdeck.git`.
- SSH private key: отдельный ключ, публичная часть которого добавлена в GitLab как read-only Deploy Key проекта.
- Для SSH настрой проверенный host key GitLab в Argo CD known hosts. Отпечаток сверяй с администратором GitLab, не отключай проверку.

Альтернатива — реальный HTTPS clone URL из GitLab, username и token с `read_repository`. Для GitLab используй URL с `.git`. Если репозиторий уже подключён и статус Successful, повторять не нужно. [Приватные репозитории и доверие сертификатам](https://argo-cd.readthedocs.io/en/stable/user-guide/private-repositories/).

Имена `gitlab.lan` и registry должны разрешаться из кластера. Для частного CA доверие настраивается отдельно: Git CA в Argo CD, registry CA на Kubernetes/Talos-нодах и на машине сборки. Настройка сертификата Git в Argo CD не исправит `ImagePullBackOff` из-за registry CA.

## 6. Создать Application и выполнить первый sync

Сохрани в `gitops/talosdeck-application.yaml` (вне каталога `gitops/talosdeck`, который читает само приложение):

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: talosdeck
  namespace: argocd
spec:
  project: default
  source:
    repoURL: git@gitlab.lan:root/talosdeck.git
    targetRevision: redesign/operator-console
    path: gitops/talosdeck
  destination:
    server: https://kubernetes.default.svc
    namespace: talosdeck
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
```

Если используешь HTTPS clone URL, измени `repoURL` на точно такой же, как при подключении репозитория. Если Argo CD разворачивает в другом кластере, укажи его зарегистрированный destination. AppProject должен разрешать этот repo, destination и типы ресурсов; пример использует `default`.

```bash
git add gitops/talosdeck-application.yaml
git commit -m "deploy: add TalosDeck Argo CD application"
git push gitlab HEAD

kubectl apply -f gitops/talosdeck-application.yaml
```

В UI Argo CD открой **talosdeck → Diff → Sync → Synchronize**. Первый sync ручной. Или после входа CLI в свой Argo CD:

```bash
argocd app sync talosdeck
argocd app wait talosdeck --sync --health --timeout 300
kubectl -n talosdeck rollout status deployment/talosdeck --timeout=300s
kubectl -n talosdeck get pods,pvc,svc
kubectl -n talosdeck logs deployment/talosdeck --tail=100
```

Статус ожидается `Synced / Healthy`, PVC — `Bound`. У StorageClass с `WaitForFirstConsumer` PVC может ждать назначения Pod до связывания.

## 7. Открыть панель и проверить версию

```bash
kubectl -n talosdeck port-forward svc/talosdeck 8080:8080
```

Открой `http://localhost:8080`, войди паролем из шага 3. Если 8080 занят локальным `make run`, используй `18080:8080` и `http://localhost:18080`.

Проверка, что загружен нужный образ:

```bash
kubectl -n talosdeck get deployment talosdeck \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n talosdeck get pods -l app=talosdeck \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].imageID}{"\n"}{end}'
```

Сравни с опубликованным тегом/digest. В новом интерфейсе — 10 разделов и отметка `Console UI 2`. Проверь ноды, workloads, логи и список бэкапов. Зелёный Pod сам по себе не подтверждает, что все подключения работают.

## 8. Обновления и откат

1. Закоммить новые исходники.
2. Задай новый `TD_TAG` по Git SHA, собери и опубликуй образ командами шага 2.
3. В `gitops/talosdeck/kustomization.yaml` замени `newTag` на опубликованный тег.
4. Закоммить и запушь изменение манифеста в ветку `targetRevision`.
5. Выполни Sync, дождись Healthy, проверь imageID и интерфейс.

**Один docker push не обновляет приложение.** Argo CD в этой схеме отслеживает Git-манифесты, а не новые теги registry.

Для отката верни предыдущий `newTag` коммитом и выполни Sync. Это откат приложения, не содержимого PVC; перед несовместимыми изменениями формата данных нужна отдельная копия данных.

После успешного первого запуска можно включить auto-sync, добавив в Application под `spec.syncPolicy`:

```yaml
automated:
  prune: false
  selfHeal: true
```

После изменения Application снова выполни `kubectl apply -f gitops/talosdeck-application.yaml`: этот родительский манифест не управляется самим приложением. После слияния UI-ветки поменяй `targetRevision` на выбранную постоянную ветку и также примени Application.

При изменении Secret с переменными окружения нужен перезапуск Pod:

```bash
kubectl -n talosdeck rollout restart deployment/talosdeck
```

## 9. Необязательные интеграции

Без Proxmox и Telegram основную панель можно запустить. Для настройки добавь ключи в `talosdeck-runtime` через защищённый env-файл и команду создания Secret из шага 3, сохраняя существующие пароль и JWT secret:

| Ключ | Значение |
|---|---|
| `PROXMOX_URL` | Реальный HTTPS URL API Proxmox |
| `PROXMOX_NODE` | Имя хоста Proxmox |
| `PROXMOX_API_TOKEN` | API token в формате `user@realm!tokenid=secret` |
| `PROXMOX_STORAGE` | Существующий storage для дисков ВМ |
| `PROXMOX_ISO` | Реальный volume ID загруженного Talos ISO |
| `PROXMOX_BRIDGE` | Существующий bridge, например `vmbr0` |
| `PROXMOX_CA_CERT` | PEM доверенного CA; для многострочного значения используй отдельный Secret/файл и `PROXMOX_CA_FILE` с его mount |
| `TELEGRAM_BOT_TOKEN` | Токен бота |
| `TELEGRAM_CHAT_ID` | ID чата |

После изменения Secret перезапусти Deployment. Значения по умолчанию Proxmox в коде относятся к конкретному стенду; не рассчитывай, что storage/ISO автоматически подходят твоему серверу.

## 10. Если не запускается

| Симптом | Что проверить |
|---|---|
| Argo CD не читает Git | Repository status, deploy key/token, DNS `gitlab.lan`, SSH known hosts или HTTPS CA, совпадение repoURL |
| `ImagePullBackOff` | Образ/тег реально опубликован, pull token имеет `read_registry`, Secret в namespace `talosdeck`, registry доступен нодам, CA доверен container runtime |
| `no matching manifest` / `exec format error` | Архитектура образа соответствует ноде |
| `CreateContainerConfigError` | Созданы все четыре Secret из шага 3, имена совпадают с патчем |
| PVC `Pending` | StorageClass/provisioner, свободное место, topology и события PVC |
| `CrashLoopBackOff` | `kubectl -n talosdeck logs deployment/talosdeck --previous`, talosconfig, сертификаты и доступ к Talos API |
| Workloads возвращают `403` | Используется явно подключённый kubeconfig, его пользователь имеет нужные права; kubeconfig не содержит недоступного exec-плагина |
| Ошибки подключения Kubernetes | Адрес server в kubeconfig доступен из Pod, CA/сертификаты действительны |
| Старый интерфейс | Ветка сборки, newTag в Git, Sync, imageID; браузер открыт на нужном порту, а не на старом `make run` |
| Логи WS обрываются через ingress | TLS/WSS, WebSocket proxy и timeout контроллера; сравни с port-forward |

Для событий без вывода содержимого секретов:

```bash
kubectl -n talosdeck get events --sort-by=.lastTimestamp
kubectl -n talosdeck describe deployment talosdeck
kubectl -n talosdeck describe pvc talosdeck-data
```

Инструкция проверена по коду и локальному рендерингу Kustomize. Реальные push, подключение репозитория и sync в кластер в рамках её подготовки не выполнялись. Открытые дефекты приложения перечислены в [BUGS.md](BUGS.md); задачи релиза — в [TASKS.md](TASKS.md).
