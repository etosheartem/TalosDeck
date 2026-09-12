# Развёртывание TalosDeck

Команды выполняются из корня репозитория. Подставьте свои пути, registry,
image tag и Git URL. Привязки к локальной сети разработчика не требуются.

## Сборка и публикация образа

Dockerfile собирает SPA, Go-бинарник и добавляет `talosctl v1.14.0`.
Для сборки используйте Docker Buildx; архитектура должна совпадать с нодами.

```bash
export TD_IMAGE=ghcr.io/etosheartem/talosdeck
export TD_TAG="$(git rev-parse --short=12 HEAD)"

docker login ghcr.io
docker buildx build --platform linux/amd64 \
  --tag "${TD_IMAGE}:${TD_TAG}" --push .
docker buildx imagetools inspect "${TD_IMAGE}:${TD_TAG}"
```

Публикация в указанный namespace требует прав владельца пакета. Для своего
fork замените `TD_IMAGE`. Для ARM используйте `linux/arm64`.
Registry-токен публикации должен иметь право записи; Kubernetes достаточно
права скачивания. Не переиспользуйте один тег для разных сборок.

## Конфигурация подключения

Подготовьте вне репозитория:

- `talosconfig` с нужным активным контекстом и доступными endpoints;
- самодостаточный kubeconfig того же кластера, со встроенными сертификатами;
- `runtime.env` с паролем администратора и отдельным случайным JWT secret.

Пример структуры `runtime.env` — значения необходимо заменить:

```dotenv
TALOSDECK_ADMIN_PASSWORD=replace-with-a-unique-password
TALOSDECK_JWT_SECRET=replace-with-a-random-secret
```

Случайное значение можно получить через `openssl rand -hex 32`.
Ограничьте доступ к файлу. Файлы конфигурации должны читаться UID 1000
в контейнере. Kubeconfig с локальным exec-плагином или адресом `127.0.0.1`
нельзя просто перенести с рабочей станции в Pod.

## Docker

```bash
docker volume create talosdeck-data
docker run -d --name talosdeck --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --env-file /absolute/path/to/runtime.env \
  -e KUBECONFIG=/etc/kubernetes/kubeconfig \
  --mount type=bind,src=/absolute/path/to/talosconfig,dst=/etc/talos/talosconfig,readonly \
  --mount type=bind,src=/absolute/path/to/kubeconfig,dst=/etc/kubernetes/kubeconfig,readonly \
  --mount type=volume,src=talosdeck-data,dst=/app/data \
  "${TD_IMAGE}:${TD_TAG}"
```

Откройте `http://localhost:8080`. Для доступа с других машин настройте
свой HTTPS reverse proxy с поддержкой WebSocket.

## Kubernetes и Argo CD

Предполагается, что Argo CD уже установлен, `kubectl` подключён к кластеру
размещения, а StorageClass может выделить PVC. Управляемый кластер
выбирается через переданные credentials.

### Namespace и секреты

```bash
kubectl apply -f deploy/namespace.yaml

kubectl -n talosdeck create secret generic talosdeck-config \
  --from-file=talosconfig=/absolute/path/to/talosconfig \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n talosdeck create secret generic talosdeck-kubeconfig \
  --from-file=kubeconfig=/absolute/path/to/kubeconfig \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n talosdeck create secret generic talosdeck-runtime \
  --from-env-file=/absolute/path/to/runtime.env \
  --dry-run=client -o yaml | kubectl apply -f -
```

Значения Secret не должны попадать в Git. Для приватного образа дополнительно
создайте registry pull Secret в namespace `talosdeck` и укажите его имя
в `spec.template.spec.imagePullSecrets` Deployment.

### Манифесты

Используйте каталог [gitops/talosdeck](../gitops/talosdeck). Он содержит
Deployment, Service, PVC и Kustomize-патчи. Перед первым sync проверьте:

- `images.newName` и `images.newTag` в `kustomization.yaml` соответствуют
  опубликованному образу;
- PVC использует существующий StorageClass или default-класс;
- имена Secret и путь kubeconfig совпадают с созданными выше;
- доступ из Pod к Talos API и Kubernetes API разрешён сетью;
- число replicas равно 1: файловый job store не является HA-хранилищем.

```bash
kubectl kustomize gitops/talosdeck
git add gitops/talosdeck
git commit -m "deploy: configure TalosDeck image"
git push
```

### Application

Подключите Git-репозиторий в Argo CD. Для приватного репозитория нужны
отдельные Git credentials; registry pull Secret их не заменяет.

Создайте Application со следующими полями, подставив URL и ветку,
в которые отправлены манифесты:

```yaml
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
```

Сохраните этот манифест отдельно от `gitops/talosdeck`, например
в `/tmp/talosdeck-application.yaml`, и примените:

```bash
kubectl apply -f /tmp/talosdeck-application.yaml
argocd app sync talosdeck
argocd app wait talosdeck --sync --health --timeout 300
kubectl -n talosdeck get pods,pvc,svc
kubectl -n talosdeck logs deployment/talosdeck --tail=100
kubectl -n talosdeck port-forward svc/talosdeck 8080:8080
```

Argo CD CLI должен быть подключён к вашему серверу. Для другой площадки
размещения укажите зарегистрированный destination. AppProject должен разрешать
выбранные repo и destination. Sync также можно выполнить через UI Argo CD.

Откройте `http://localhost:8080`. Если порт занят, используйте
`18080:8080` и откройте `http://localhost:18080`.

## Обновление панели

Соберите и опубликуйте новый образ, измените `newTag` в GitOps-манифесте,
отправьте коммит и выполните Sync. Один push образа не меняет Deployment.

Текущая `.gitlab-ci.yml` автоматизирует сборку в GHCR и изменение image tag
для настроенного GitLab-окружения; это не GitHub Actions workflow.

Проверка фактически запущенного образа:

```bash
kubectl -n talosdeck get deployment talosdeck \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n talosdeck get pods -l app=talosdeck \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].imageID}{"\n"}{end}'
```

Откат приложения — вернуть предыдущий тег коммитом и выполнить Sync.
Это не откатывает данные на PVC. При изменении runtime Secret перезапустите
Deployment, чтобы процесс получил новые переменные окружения.

## Данные и интеграции

В `/app/data` хранятся резервные копии, аудит и задания. Сохраняйте этот
каталог между перезапусками. Копии для аварийного восстановления должны
быть доступны вне управляемого кластера.

Proxmox настраивается через `PROXMOX_URL`, `PROXMOX_NODE`,
`PROXMOX_API_TOKEN`, `PROXMOX_STORAGE`, `PROXMOX_ISO`, `PROXMOX_BRIDGE`.
Токен: `user@realm!tokenid=secret`. Для собственного CA используйте
`PROXMOX_CA_FILE` с подключённым файлом. Telegram — через
`TELEGRAM_BOT_TOKEN` и `TELEGRAM_CHAT_ID`. Секретные значения передавайте
через runtime Secret/файл окружения.

## Диагностика запуска

| Симптом | Проверить |
|---|---|
| `ImagePullBackOff` | Image/tag, права pull, DNS и доверие CA registry на нодах |
| PVC `Pending` | StorageClass, provisioner, место и ограничения размещения |
| `CreateContainerConfigError` | Наличие и имена Secret |
| `CrashLoopBackOff` | Логи предыдущего контейнера, конфиги и права на каталог данных |
| Workloads возвращают `403` | Явный kubeconfig, его permissions и выбранный кластер |
| Видна только часть нод | Talos discovery/Members; при необходимости полный список `nodes` в talosconfig |
| Старый интерфейс | ImageID, тег в манифесте, Sync и правильный адрес панели |

```bash
kubectl -n talosdeck get events --sort-by=.lastTimestamp
kubectl -n talosdeck describe deployment talosdeck
kubectl -n talosdeck describe pvc talosdeck-data
```

Обновления управляемого кластера: [операции и задания](operations.md).
