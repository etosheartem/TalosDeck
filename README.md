# TalosDeck

**Lightweight open-source self-hosted management platform for Talos Linux.**

TalosDeck — панель управления Talos Linux и Kubernetes. Go-бэкенд подключается
к Talos через mTLS/gRPC, к Kubernetes через API, а Vue-интерфейс встроен
в бинарник. Для работы с нодами SSH не требуется.

Проект развивается. Текущая установка работает с одним кластером;
multi-cluster и полное создание кластеров пока не входят в поставку.

## Возможности

- Обнаружение нод, роли, версии, состояние сервисов и ресурсы машин.
- Просмотр Kubernetes-подов и namespaces, дисков и разделов нод.
- Просмотр MachineConfig, потоковые логи ядра и Talos services.
- Перезагрузка, обслуживание нод и перезапуск сервисов.
- Снимки etcd и архивы конфигурации с локальным хранением.
- Фоновые задания для обновлений Talos/Kubernetes и rolling reboot,
  предварительные проверки и сохраняемый журнал выполнения.
- Интеграция с Proxmox для создания и удаления worker VM.
- Вход администратора, аудит действий и Telegram-уведомления.
- Русский и английский интерфейс.

Фоновые обновления используют штатный `talosctl`, включённый в Docker-образ.
Применение MachineConfig, автоматическое расписание/восстановление бэкапов,
OIDC и HA здесь не заявляются как готовые функции.

## Запуск

Понадобятся административный `talosconfig`, kubeconfig нужного кластера
и сетевой доступ к их API. Для управления при аварии кластера размещайте
TalosDeck на отдельной машине или в management-кластере.

- [Сборка образа, Docker, Kubernetes и Argo CD](docs/deployment.md).
- [Обновления, фоновые задания и прерывания](docs/operations.md).

## Разработка

Требования: Go согласно [go.mod](go.mod), Bun и `talosctl v1.14.0`
для выполнения фоновых операций. Версии инструментов контейнерной сборки
зафиксированы в [Dockerfile](Dockerfile).

```bash
git clone https://github.com/etosheartem/TalosDeck.git
cd TalosDeck
make build
```

Задайте `TALOSDECK_ADMIN_PASSWORD` и `TALOSDECK_JWT_SECRET` в окружении,
после чего запустите:

```bash
TALOSCONFIG=/absolute/path/to/talosconfig \
KUBECONFIG=/absolute/path/to/kubeconfig \
./bin/talosdeck
```

Панель доступна на `http://localhost:8080`. Runtime-данные сохраняются в `data/`.
Kubeconfig должен указывать на тот же кластер, что и talosconfig.

Для разработки интерфейса:

```bash
cd web
bun install --frozen-lockfile
bun run dev
```

## Проверки

```bash
go test ./...
go vet ./...
go test -race ./internal/jobs ./internal/operations
cd web
bun run build
bun run test:ui
```

Браузерный smoke-тест использует Chromium (`CHROMIUM_PATH`, по умолчанию
`/usr/bin/chromium`) и подменённые ответы API. Он не выполняет обновления
реального кластера.

## Структура

```text
cmd/talosdeck/       Точка входа приложения
internal/           API, клиенты инфраструктуры и подсистемы backend
web/                Vue SPA, frontend-тесты и go:embed
deploy/             Базовые Kubernetes-манифесты
gitops/talosdeck/    Kustomize-манифесты для Argo CD
docs/               Пользовательская документация
Dockerfile          Сборка контейнера
Makefile            Локальная сборка и запуск
```

`bin/`, `data/`, `web/dist/` и `web/node_modules/` создаются локально и не входят
в Git. Данные подключения и секреты храните вне checkout.

Исходный код: [github.com/etosheartem/TalosDeck](https://github.com/etosheartem/TalosDeck).
