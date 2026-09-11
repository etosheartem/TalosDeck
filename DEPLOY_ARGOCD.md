# TalosDeck: образ в GitHub Container Registry (GHCR) и запуск через Argo CD

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

# Задайте пароль администратора (замените на свой пароль):
export TD_ADMIN_PASSWORD="ВашНадежныйПароль123!"

printf 'TALOSDECK_ADMIN_PASSWORD=%s\n' "$TD_ADMIN_PASSWORD" > "$TD_SECRET_DIR/runtime.env"
printf 'TALOSDECK_JWT_SECRET=%s\n' "$(openssl rand -hex 32)" >> "$TD_SECRET_DIR/runtime.env"
unset TD_ADMIN_PASSWORD

kubectl -n talosdeck create secret generic talosdeck-runtime \
  --from-env-file="$TD_SECRET_DIR/runtime.env" \
  --dry-run=client -o yaml | kubectl apply -f -

# Секрет для скачивания из GHCR (если пакет в GitHub сделан Public, этот шаг можно пропустить):
kubectl -n talosdeck create secret docker-registry talosdeck-registry \
  --docker-server=ghcr.io \
  --docker-username=etosheartem \
  --docker-password="$TD_PUSH_TOKEN" \
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
git push origin main
```

Каталог — копия базовых манифестов: дальнейшие изменения поставки вноси в `gitops/talosdeck`, иначе Argo CD их не увидит. [Как Argo CD обрабатывает Kustomize](https://argo-cd.readthedocs.io/en/stable/user-guide/kustomize/).

## 5. Подключить GitHub к Argo CD

Доступ к Git и скачивание образов — это разные вещи. Если ваш репозиторий на GitHub **публичный**, Argo CD клонирует его **без каких-либо настроек и секретов**!

Если репозиторий GitHub **приватный**, добавьте его в Argo CD:

### Через веб-интерфейс Argo CD:
1. В UI Argo CD перейдите в **Settings → Repositories → Connect Repo**.
2. Укажите:
   - **Choose your connection method**: `VIA HTTPS`
   - **Type**: `git`
   - **Repository URL**: `https://github.com/etosheartem/TalosDeck.git`
   - **Username**: `etosheartem`
   - **Password**: ваш GitHub Personal Access Token (с правом `repo`)
3. Нажмите **CONNECT** (статус должен стать `Successful`).

### Либо через секрет Kubernetes:
```bash
kubectl apply -n argocd -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: repo-talosdeck-github
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: https://github.com/etosheartem/TalosDeck.git
  username: etosheartem
  password: "ВАШ_GITHUB_PAT"
EOF
```

## 6. Создать Application в Argo CD и синхронизировать

### Что такое Application простыми словами:
`Application` в Argo CD — это задача для GitOps. Вы просто говорите Argo CD:  
> *«Следи за репозиторием `https://github.com/etosheartem/TalosDeck.git` (ветка `main`, папка `gitops/talosdeck`), и всё, что там написано, разверни в наш кластер в неймспейс `talosdeck`»*.

Создать его можно **любым из двух способов**:

---

### Способ 1. Одной командой в терминале (Рекомендуется)

Выполните одну команду, которая создаст Application напрямую в Argo CD:

```bash
kubectl apply -n argocd -f - <<'EOF'
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: talosdeck
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/etosheartem/TalosDeck.git
    targetRevision: main
    path: gitops/talosdeck
  destination:
    server: https://kubernetes.default.svc
    namespace: talosdeck
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
EOF
```

*(Примечание: если манифесты запушены в локальный GitLab, а не GitHub, просто замените `repoURL` на `http://gitlab.lan:8080/root/talosdeck.git`)*.

---

### Способ 2. Либо через веб-интерфейс Argo CD (кнопками):
1. Откройте веб-интерфейс Argo CD: `http://10.42.0.110:30080`
2. Нажмите синюю кнопку **+ NEW APP** (вверху слева).
3. Заполните поля:
   - **Application Name**: `talosdeck`
   - **Project Name**: `default`
   - **Sync Policy**: `Manual` (или `Automatic`)
   - **Repository URL**: `https://github.com/etosheartem/TalosDeck.git`
   - **Revision**: `main`
   - **Path**: `gitops/talosdeck`
   - **Cluster URL**: `https://kubernetes.default.svc`
   - **Namespace**: `talosdeck`
4. Нажмите **CREATE** вверху.

---

### Первый запуск (Sync):
1. На главной странице Argo CD появится карточка приложения **`talosdeck`** со статусом `OutOfSync` (желтый круг).
2. Нажмите на карточку приложения, затем нажмите кнопку **SYNC** вверху и подтвердите: **SYNCHRONIZE**.
3. Argo CD скачает манифесты из Git и создаст Deployment, Pod, Service и PVC.
4. Через 10–20 секунд кружок станет зелёным: **`Synced`** и **`Healthy`**.

---

## 7. Открыть панель TalosDeck в браузере

После того как Argo CD засинхронизировал приложение, пробросьте порт на рабочую машину:

```bash
kubectl -n talosdeck port-forward svc/talosdeck 8080:8080
```

1. Откройте в браузере: 👉 **`http://localhost:8080`**
2. Введите:
   - **Логин**: `admin`
   - **Пароль**: `admin` *(пароль, который мы записали в секрет `talosdeck-runtime`)*
3. Вы попадёте в новый интерфейс оператора TalosDeck с 10 разделами!

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
| Argo CD не читает Git | Repository status в Argo CD, доступность GitHub, права токена (repo), точное совпадение repoURL и targetRevision: main |
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
