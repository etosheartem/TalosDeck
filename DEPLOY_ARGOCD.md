# 🚀 TalosDeck: Полное пошаговое руководство по установке через Argo CD с нуля

Данное руководство проверено на реальном кластере и описывает чистую установку **TalosDeck** с нуля в среде:
- **Kubernetes**: v1.37.0 на Talos Linux v1.14.0 (ноды `10.42.0.110`, `10.42.0.111`, `10.42.0.112`)
- **GitOps-контроллер**: Argo CD (веб-интерфейс `http://10.42.0.110:30080`)
- **Git-репозиторий манифестов**: Локальный GitLab (`http://10.42.0.238:8080/root/talosdeck.git`)
- **Реестр Docker-образов**: GitHub Container Registry (`ghcr.io/etosheartem/talosdeck:latest`)

---

## 🏗 Архитектура поставки

```text
[Исходный код TalosDeck]
        │
        ├── 1. docker build & push ───────────► [GitHub Container Registry (GHCR)]
        │                                         (публичный образ talosdeck:latest)
        │                                                         │
        └── 2. git push (папка gitops/)                           │ 4. docker pull
                    │                                             ▼
                    ▼                                   ┌───────────────────┐
         [Локальный GitLab]                             │  Кластер Talos    │
     (10.42.0.238:8080/root/talosdeck)                  │  Namespace:       │
                    ▲                                   │  talosdeck        │
                    │ 3. опрос манифестов               │                   │
                    │                                   │  [Pod: talosdeck] │
         [Argo CD Controller] ─────────────────────────►│  [PVC: 20 GiB]   │
        (http://10.42.0.110:30080)        Sync          └───────────────────┘
```

---

## Предварительные требования

Перед началом убедитесь, что в кластере работают базовые компоненты:

1. **StorageClass `local-path`**:
   Talos Linux имеет неизменяемую rootfs, поэтому стандартный каталог `/opt` недоступен. Хранилище должно быть установлено с путями в `/var/local-path-provisioner`:
   ```bash
   kubectl --kubeconfig=/home/artem/laba-kuber/kubeconfig apply -f /home/artem/laba-kuber/platform-services/storage/local-path-storage.yaml
   ```
   Проверка: `kubectl get sc` (должен быть `local-path (default)`).

2. **Argo CD**:
   Запущен в пространстве имён `argocd`. Доступен по адресу `http://10.42.0.110:30080`.

---

## Шаг 1. Создание namespace и секретов

Для работы панели управления требуются административные сертификаты Talos API, kubeconfig и переменные рантайма (JWT и пароль администратора).

Выполните команды в терминале:

```bash
KUBECONFIG_PATH="/home/artem/laba-kuber/kubeconfig"
TALOSCONFIG_PATH="/home/artem/laba-kuber/cluster-config/talosconfig"

# 1. Создаем namespace
kubectl --kubeconfig=$KUBECONFIG_PATH create namespace talosdeck --dry-run=client -o yaml | kubectl apply -f -

# 2. Секрет с конфигурацией Talos (mTLS сертификаты нод)
kubectl --kubeconfig=$KUBECONFIG_PATH -n talosdeck create secret generic talosdeck-config \
  --from-file=talosconfig="$TALOSCONFIG_PATH" \
  --dry-run=client -o yaml | kubectl apply -f -

# 3. Секрет с kubeconfig (доступ панели к Kubernetes API)
kubectl --kubeconfig=$KUBECONFIG_PATH -n talosdeck create secret generic talosdeck-kubeconfig \
  --from-file=kubeconfig="$KUBECONFIG_PATH" \
  --dry-run=client -o yaml | kubectl apply -f -

# 4. Секрет с паролем администратора и ключом сессий JWT
# (Укажите желаемый пароль вместо "admin")
kubectl --kubeconfig=$KUBECONFIG_PATH -n talosdeck create secret generic talosdeck-runtime \
  --from-literal=TALOSDECK_ADMIN_PASSWORD="admin" \
  --from-literal=TALOSDECK_JWT_SECRET="$(openssl rand -hex 32)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Проверьте созданные секреты:
```bash
kubectl --kubeconfig=$KUBECONFIG_PATH -n talosdeck get secrets
# Ожидаемый вывод:
# talosdeck-config       Opaque   1
# talosdeck-kubeconfig   Opaque   1
# talosdeck-runtime      Opaque   2
```

---

## Шаг 2. Сборка и публикация образа в GHCR

Образ собирается через Docker Buildx под архитектуру нод кластера (`linux/amd64`) со встроенным фронтендом.

### 1. Авторизация в GitHub Container Registry:
```bash
docker login ghcr.io -u etosheartem
```
*(При появлении запроса `Password:` вставьте ваш GitHub Personal Access Token `ghp_...` с правом `write:packages`)*.

### 2. Сборка и push:
```bash
cd /home/artem/laba-kuber/TalosDeck

docker buildx build --platform linux/amd64 \
  --tag ghcr.io/etosheartem/talosdeck:v0.1.0 \
  --tag ghcr.io/etosheartem/talosdeck:latest \
  --push .
```

### 3. Важно: сделать пакет публичным (Public)
1. Откройте в браузеру страницу пакета:  
   👉 **[https://github.com/users/etosheartem/packages/container/package/talosdeck](https://github.com/users/etosheartem/packages/container/package/talosdeck)**
2. Внизу справа нажмите **Package settings**.
3. В самом низу в блоке **Danger Zone** нажмите **Change package visibility** → выберите **Public**.
*(Это позволит кластеру скачивать образ без создания секретов авторизации)*.

---

## Шаг 3. Подготовка GitOps-манифестов

В репозитории проекта создаётся изолированная папка `gitops/talosdeck`, за которой будет следить Argo CD.

```bash
cd /home/artem/laba-kuber/TalosDeck
mkdir -p gitops/talosdeck

# Копируем базовые шаблоны
cp deploy/deployment.yaml deploy/service.yaml deploy/pvc.yaml gitops/talosdeck/
```

Создайте файл `gitops/talosdeck/kustomization.yaml`:

```bash
cat <<'EOF' > gitops/talosdeck/kustomization.yaml
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
images:
  - name: talosdeck
    newName: ghcr.io/etosheartem/talosdeck
    newTag: "latest"
EOF
```

Зафиксируйте манифесты в локальном GitLab:
```bash
git add gitops/talosdeck
git commit -m "deploy: configure Argo CD Kustomize manifests"
git push gitlab main
```

---

## Шаг 4. Подключение к Argo CD и развертывание (`Application`)

### Почему используется адрес `10.42.0.238:8080`:
Контейнер GitLab в Proxmox имеет внутренний IP **`10.42.0.238`**, находящийся в той же виртуальной сети моста `vmbr0`, что и ноды кластера. Обращение по внутреннему IP работает напрямую без проблем с NAT и не требует авторизации.

Создайте приложение Argo CD одной командой:

```bash
kubectl --kubeconfig=/home/artem/laba-kuber/kubeconfig apply -n argocd -f - <<'EOF'
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: talosdeck
  namespace: argocd
spec:
  project: default
  source:
    repoURL: http://10.42.0.238:8080/root/talosdeck.git
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

---

## Шаг 5. Синхронизация (Sync)

### Вариант 1. Через веб-интерфейс Argo CD:
1. Откройте в браузере: 👉 **[http://10.42.0.110:30080](http://10.42.0.110:30080)**  
   *(Логин: `admin`, пароль: получить командой `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d; echo`)*.
2. Нажмите на плитку приложения **`talosdeck`**.
3. Вверху нажмите **SYNC** → подтвердите **SYNCHRONIZE**.

### Вариант 2. Через терминал (CLI):
```bash
kubectl --kubeconfig=/home/artem/laba-kuber/kubeconfig patch application talosdeck -n argocd \
  --type merge -p '{"operation": {"sync": {"prune": false}}}'
```

Через 10–15 секунд статус приложения станет:  
✅ **`Synced`** / **`Healthy`**

---

## Шаг 6. Запуск и проверка работы панели

### 1. Проверьте состояние ресурсов:
```bash
kubectl --kubeconfig=/home/artem/laba-kuber/kubeconfig -n talosdeck get pods,pvc,svc
```
Ожидаемый вывод:
```text
NAME                            READY   STATUS    RESTARTS   AGE
pod/talosdeck-xxxxxxxxxx-xxxxx  1/1     Running   0          30s

NAME                                   STATUS   VOLUME      CAPACITY   ACCESS MODES   STORAGECLASS
persistentvolumeclaim/talosdeck-data   Bound    pvc-xxxxx   20Gi       RWO            local-path

NAME                TYPE        CLUSTER-IP       EXTERNAL-IP   PORT(S)
service/talosdeck   ClusterIP   10.109.207.8     <none>        8080/TCP
```

### 2. Проброс порта и вход в систему:
```bash
kubectl --kubeconfig=/home/artem/laba-kuber/kubeconfig -n talosdeck port-forward svc/talosdeck 8080:8080
```

1. Откройте браузер: 👉 **[http://localhost:8080](http://localhost:8080)**
2. Введите учетные данные:
   - **Логин**: `admin`
   - **Пароль**: `admin` *(или пароль, указанный вами на шаге 1 в `talosdeck-runtime`)*

Панель **TalosDeck Console UI** загрузится со всеми 10 разделами управления кластером!

---

## 🔄 Как обновлять приложение в будущем

Благодаря связке Docker Build + GitOps обновление делается по простой схеме:

1. Вы вносите изменения в код TalosDeck.
2. Пересобираете и заливаете образ:
   ```bash
   docker buildx build --platform linux/amd64 --tag ghcr.io/etosheartem/talosdeck:latest --push .
   ```
3. Перезапускаете под в кластере:
   ```bash
   kubectl -n talosdeck rollout restart deployment/talosdeck
   ```
   *(либо коммитите новый тег в `gitops/talosdeck/kustomization.yaml`, и Argo CD обновляет под автоматически)*.

---

## 🛠 Устранение типичных неполадок

| Проблема / Ошибка | Причина | Решение |
| :--- | :--- | :--- |
| **`401 Unauthorized` при pull** | Пакет в GHCR закрыт приватным доступом | Переключить видимость пакета в GitHub: **Package Settings → Danger Zone → Change visibility → Public** |
| **`NotFound / tag not found`** | В `kustomization.yaml` указан тег, которого нет в GHCR | Указать существующий тег (например, `latest` или `v0.1.0`) в `newTag` |
| **`Connection refused` к GitLab** | Запрос идёт через внешний IP с неработающим hairpin NAT | Использовать прямой внутренний адрес `http://10.42.0.238:8080/root/talosdeck.git` |
| **PVC `Pending`** | Не установлен StorageClass по умолчанию | Установить `local-path-storage.yaml` и пометить namespace меткой `pod-security.kubernetes.io/enforce=privileged` |
| **`CrashLoopBackOff`** | Неверный kubeconfig или talosconfig | Проверить логи: `kubectl -n talosdeck logs deployment/talosdeck --previous` |
