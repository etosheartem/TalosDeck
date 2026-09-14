# Развёртывание TalosDeck

TalosDeck — один контейнер с API, встроенным интерфейсом и `talosctl`. Можно начать с пустого реестра и импортировать кластеры через UI. Размещайте панель на отдельной VM или в management-кластере, чтобы она оставалась доступной при поломке управляемого Kubernetes.

SQLite и файловые jobs требуют **одной реплики**. Helm использует `Recreate`; несколько процессов не должны открывать общую БД.

## Право выполнения изменений

После обновления TD-31 production binary допускает инфраструктурные изменения только
с независимым SSH execution authority. Без него доступны чтение и вход, mutations
возвращают HTTP 423. Перед обновлением рабочей установки подготовьте authority,
SSH key/known_hosts и постоянный state вне management VM по
[инструкции восстановления](management-recovery.md#independent-execution-authority-required-for-mutations).
Не запускайте одновременно прежний unfenced binary и новый executor.

## Образ

Из корня репозитория, заменив namespace для своего fork:

```bash
export TD_IMAGE=ghcr.io/etosheartem/talosdeck
export TD_TAG="$(git rev-parse --short=12 HEAD)"
docker login ghcr.io
docker buildx build --platform linux/amd64 --tag "${TD_IMAGE}:${TD_TAG}" --push .
docker buildx imagetools inspect "${TD_IMAGE}:${TD_TAG}"
```

Для ARM используйте `linux/arm64`. Публикация требует прав записи в пакет. Используйте неизменяемый тег коммита, не `latest`. Dockerfile собирает SPA, Go и проверяет SHA-256 скачиваемого `talosctl`. Локально, без публикации: `docker build -t talosdeck:local .`.

## Docker Compose

Нужны Docker Engine и плагин `docker compose`. Создайте приватный файл **вне репозитория**:

```bash
install -d -m 700 "$HOME/.config/talosdeck"
umask 077
printf 'TALOSDECK_ADMIN_PASSWORD=%s\n' "$(openssl rand -hex 24)" \
  > "$HOME/.config/talosdeck/runtime.env"
printf 'TALOSDECK_IMAGE=%s:%s\nTALOSDECK_PORT=8080\n' "$TD_IMAGE" "$TD_TAG" \
  >> "$HOME/.config/talosdeck/runtime.env"
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" up -d
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" ps
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

Для локальной сборки задайте `TALOSDECK_IMAGE=talosdeck:local`. Откройте `http://127.0.0.1:8080`, войдите как `admin` с сохранённым паролем. Затем **Clusters → Import cluster**: имя, talosconfig и kubeconfig одного кластера со встроенными сертификатами. Локальные exec-плагины, ссылки на файлы и отключённая TLS-проверка kubeconfig не принимаются. Backend проверяет оба API и соответствие Kubernetes CA данным Talos.

Пароль из env используется только для создания первого администратора. После создания пользователей изменение env не перезаписывает их пароли. JWT secret автоматически сохраняется зашифрованным; при необходимости задайте постоянный `TALOSDECK_JWT_SECRET`.

Compose привязывает порт к loopback. Для удалённых пользователей настройте HTTPS reverse proxy с WebSocket; адрес привязки меняется через `TALOSDECK_BIND_ADDRESS`. Данные и ключ находятся в отдельных volumes `talosdeck_talosdeck-data` и `talosdeck_talosdeck-keys`. Приложение работает как UID/GID 1000, с read-only filesystem и временными конфигами в tmpfs.

```bash
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" restart talosdeck
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" down
```

Обе команды сохраняют пользователей, кластеры, jobs и backups. **Не добавляйте `down --volumes`**, если данные нужно сохранить.

## Helm

Нужны Helm, kubectl, StorageClass и доступ нод к registry. Выберите management-кластер через kubeconfig, затем:

```bash
kubectl create namespace talosdeck
kubectl -n talosdeck create secret generic talosdeck-auth \
  --from-env-file="$HOME/.config/talosdeck/runtime.env"
helm lint deploy/helm/talosdeck
helm upgrade --install talosdeck deploy/helm/talosdeck \
  --namespace talosdeck \
  --set image.repository="$TD_IMAGE" --set image.tag="$TD_TAG" \
  --set auth.existingSecret=talosdeck-auth --wait --timeout 5m
kubectl -n talosdeck get pods,pvc,svc
kubectl -n talosdeck port-forward svc/talosdeck 8080:8080
```

Создаются отдельные PVC для данных (10 GiB) и ключа (64 MiB); реестр изначально пуст. При необходимости задайте `persistence.storageClass` и `encryption.storageClass`. Существующие тома подключаются через `persistence.existingClaim` и `encryption.existingClaim`.

Для приватного registry создайте pull Secret и передайте `--set 'imagePullSecrets[0].name=registry-credentials'`. Не помещайте пароли в values или Git. ServiceAccount Pod не получает автоматический доступ к управляемому кластеру: используется импортированный kubeconfig.

Необязательный импорт при первом запуске:

```bash
kubectl -n talosdeck create secret generic talosdeck-bootstrap \
  --from-file=talosconfig=/absolute/private/path/talosconfig \
  --from-file=kubeconfig=/absolute/private/path/kubeconfig
# Добавьте к helm upgrade: --set bootstrap.existingSecret=talosdeck-bootstrap
```

Готовый JSON keyring можно подключить через `encryption.existingSecret`, ключ `master.key`. Это keyring TalosDeck, не произвольный пароль. Для CLI-ротации нужен writable key volume; Secret монтируется read-only.

Ingress: `ingress.enabled`, `className`, `host`, `tlsSecret`. Контроллер должен поддерживать WebSocket. OIDC: `oidc.enabled`, issuer, clientID, redirectURL, группы ролей; client secret хранится в `auth.existingSecret`. См. [authentication](authentication.md).

## Argo CD

Argo может управлять chart `deploy/helm/talosdeck` либо Kustomize-каталогом `gitops/talosdeck`. Runtime и registry Secrets создайте отдельно. Для Helm используйте Application в своём GitOps-репозитории:

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
    path: deploy/helm/talosdeck
    helm:
      releaseName: talosdeck
      values: |
        image:
          repository: ghcr.io/etosheartem/talosdeck
          tag: REPLACE_WITH_COMMIT_TAG
        auth:
          existingSecret: talosdeck-auth
  destination:
    server: https://kubernetes.default.svc
    namespace: talosdeck
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
```

Доступ Argo к приватному Git настраивается отдельно от registry credentials. Выполните Sync и проверьте PVC, Pod и фактический imageID. GitLab pipeline выполняет frontend smoke, Go tests/vet, публикацию образа и изменение Kustomize image tag. Для Helm Application меняйте `image.tag` в своём GitOps-репозитории.

GitLab публикует тег `CI_COMMIT_SHORT_SHA` (8 символов). Сокращённый хеш из `git log` может иметь другую длину: используйте точный опубликованный тег. Приведённая выше ручная сборка отдельно создаёт 12-символьный тег.

## Backup, обновление и откат панели

Сохраняйте **data volume и соответствующий keyring**. В data находятся SQLite, jobs, аудит и backups. Без ключа зашифрованные credentials не восстановить. Копию ключа храните отдельно, с ограниченным доступом.

Для согласованной полной копии Compose остановите приложение и упакуйте оба тома проверенным runtime image:

```bash
export TD_BACKUP="$HOME/talosdeck-backup-$(date +%Y%m%d-%H%M%S)"
install -d -m 700 "$TD_BACKUP"
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" stop talosdeck
docker run --rm --network none --user 0:0 --entrypoint tar \
  -v talosdeck_talosdeck-data:/source:ro -v "$TD_BACKUP":/backup \
  "${TD_IMAGE}:${TD_TAG}" -C /source -czf /backup/data.tgz .
docker run --rm --network none --user 0:0 --entrypoint tar \
  -v talosdeck_talosdeck-keys:/source:ro -v "$TD_BACKUP":/backup \
  "${TD_IMAGE}:${TD_TAG}" -C /source -czf /backup/keys.tgz .
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" start talosdeck
```

Восстанавливайте архивы в **новые пустые тома**, сохраняя владельца. Пример для отдельного recovery-проекта:

```bash
docker volume create talosdeck-recovery_talosdeck-data
docker volume create talosdeck-recovery_talosdeck-keys
docker run --rm --network none --user 0:0 --entrypoint tar \
  -v talosdeck-recovery_talosdeck-data:/target -v "$TD_BACKUP":/backup:ro \
  "${TD_IMAGE}:${TD_TAG}" -C /target -xzf /backup/data.tgz
docker run --rm --network none --user 0:0 --entrypoint tar \
  -v talosdeck-recovery_talosdeck-keys:/target -v "$TD_BACKUP":/backup:ro \
  "${TD_IMAGE}:${TD_TAG}" -C /target -xzf /backup/keys.tgz
TALOSDECK_PORT=18080 docker compose -p talosdeck-recovery \
  --env-file "$HOME/.config/talosdeck/runtime.env" up -d
```

После проверки можно переключить reverse proxy. Не используйте исходный и восстановленный экземпляры для параллельного управления одной инфраструктурой.

Для обновления Compose поменяйте тег в env, выполните `docker compose ... pull` и `docker compose ... up -d`. Для Helm — `helm upgrade`, для Argo — коммит и Sync.

**Откат image не откатывает схему БД.** Старый бинарник должен отказать в открытии более новой схемы. Не редактируйте номер миграции для обхода защиты. При несовместимости восстанавливайте согласованный snapshot данных и ключа, созданный перед обновлением, вместе с прежним образом. Без такого snapshot гарантировать downgrade нельзя. Незавершённые jobs после рестарта становятся interrupted и требуют проверки, а не повторяются автоматически.

## Диагностика

```bash
docker compose -p talosdeck --env-file "$HOME/.config/talosdeck/runtime.env" logs --tail=100 talosdeck
kubectl -n talosdeck logs deployment/talosdeck --tail=100
kubectl -n talosdeck get events --sort-by=.lastTimestamp
kubectl -n talosdeck get pods -l app.kubernetes.io/name=talosdeck \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].imageID}{"\n"}{end}'
```

| Симптом | Проверка |
|---|---|
| ImagePullBackOff | Тег, pull credentials, DNS и CA registry |
| PVC Pending | StorageClass, provisioner, ёмкость и размещение |
| БД заблокирована | Одна реплика, завершён ли прежний процесс |
| Не расшифровываются credentials | Соответствует ли keyring snapshot базы |
| Старый интерфейс | ImageID, image tag, Argo Sync и адрес панели |
| Частичный backup | Недоступные ноды в результате; частичный архив не равен полной копии |

Proxmox подключается через **Providers**, Telegram и backup targets — для выбранного кластера. Legacy-переменные окружения не заменяют настройки импортированных кластеров. Дальше: [операции](operations.md), [provisioning](provisioning.md).
