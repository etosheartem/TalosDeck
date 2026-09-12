# TalosDeck

**Lightweight open-source self-hosted management platform for Talos Linux.**

TalosDeck — панель управления Talos Linux и Kubernetes. Go-бэкенд подключается
к Talos через mTLS/gRPC, к Kubernetes через API, а Vue-интерфейс встроен
в бинарник. Для работы с нодами SSH не требуется.

Одна установка управляет несколькими кластерами. TalosDeck можно запустить
вне управляемой инфраструктуры через Docker Compose или в management-кластере через Helm.

## Возможности

- Обнаружение нод, роли, версии, состояние сервисов и ресурсы машин.
- Импорт кластеров, отдельные клиенты, задания, бэкапы и аудит каждого кластера.
- SQLite-хранилище с зашифрованными credentials и отдельным master key.
- Kubernetes: pods, deployments, daemonsets, statefulsets, jobs, cronjobs,
  события, контейнерные логи и связи Pod → PVC → PV → StorageClass.
- MachineConfig: patch, проверка, очищенный diff, история и восстановление ревизий.
- Потоковые логи ядра и Talos services.
- Перезагрузка, обслуживание нод и перезапуск сервисов.
- Снимки etcd и архивы конфигурации: local/S3/MinIO, расписание,
  retention, проверка целостности и восстановление etcd через отдельный план.
- Фоновые задания для обновлений Talos/Kubernetes и rolling reboot,
  предварительные проверки и сохраняемый журнал выполнения.
- Proxmox: создание кластера, добавление workers, учёт созданных VM
  и удаление с проверкой принадлежности.
- Диагностика здоровья и очищенный support bundle.
- Локальные пользователи, OIDC/Keycloak, роли Viewer/Operator/Administrator,
  отзыв сессий и аудит действий.
- Центр алертов: активные проблемы, восстановление, временная тишина;
  Telegram/Slack/Discord/Webhook/Email, маршруты и журнал доставки.
- Мониторинг сертификатов Talos/Kubernetes: сроки credentials, CA и API endpoints,
  предупреждения за 30/14/7 дней и отдельное состояние неполной проверки.
- Talos Image Factory: выбор версии и официальных extensions, проверка schematic,
  согласованные ISO/installer и просмотр установленного образа ноды.
- Русский и английский интерфейс.

Фоновые обновления используют штатный `talosctl`, включённый в Docker-образ.
Установка рассчитана на одну реплику с SQLite. HA и управление удалёнными
кластерами через агент за NAT не входят в эту версию.

## Запуск

Понадобятся административный `talosconfig`, kubeconfig нужного кластера
и сетевой доступ к их API. Для управления при аварии кластера размещайте
TalosDeck на отдельной машине или в management-кластере.

- [Сборка образа, Docker, Kubernetes и Argo CD](docs/deployment.md).
- [Навигация, поиск и ежедневная работа](docs/interface.md).
- [Обновления, фоновые задания и прерывания](docs/operations.md).
- [Пользователи, роли и OIDC](docs/authentication.md).
- [Proxmox и создание кластеров](docs/provisioning.md).
- [Образы Talos, schematics и extensions](docs/images.md).
- [Резервные копии и восстановление](docs/backups.md).
- [Сроки сертификатов и обновление credentials](docs/certificates.md).
- [Алерты, каналы и доставка уведомлений](docs/notifications.md).
- [Объём поставки и ограничения](docs/release-notes.md).

## Первое подключение

1. Войдите как Administrator и откройте **Кластеры → Добавить кластер**.
2. Укажите понятное имя и конфиги Talos и Kubernetes одного кластера.
   TalosDeck проверяет оба подключения; доступ к API нужен с машины,
   на которой запущен backend, а не только из вашего браузера.
3. В обзоре сверьте количество нод, их роли и версии. Выбранный кластер
   отображается в меню; задания и данные других кластеров отделены от него.
4. Откройте **Диагностика → Проверить кластер**. Дождитесь задания в журнале
   и вернитесь к отчёту. Недоступная подсистема означает отсутствие данных,
   а не подтверждение её исправности.
5. До первого изменения инфраструктуры создайте резервную копию и настройте
   её хранение вне управляемого кластера по [инструкции](docs/backups.md).
   Снимок etcd не заменяет резервное копирование данных PVC.

Для ежедневного чтения создайте Viewer, для разрешённых операций — Operator.
При ошибке операции откройте её журнал: статус `interrupted` требует проверки
фактического состояния нод, а не немедленного повторного запуска.

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
Существующая конфигурация импортируется один раз. После этого подключения
берутся из зашифрованной БД. Можно запустить сервер без конфигов и добавить
кластеры после входа через интерфейс.

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
go test -race ./internal/clusters ./internal/jobs ./internal/operations ./internal/api
cd web
bun run build
bun run test:ui
```

Браузерный smoke-тест использует Chromium (`CHROMIUM_PATH`, по умолчанию
`/usr/bin/chromium`) и подменённые ответы API. Он не выполняет обновления
реального кластера.

Live-тесты backup запускаются только при явном задании
`TALOSDECK_TEST_TALOSCONFIG` и `TALOSDECK_TEST_NODE`. Обычный `go test ./...`
не ищет credentials разработчика и не создаёт снимки его кластера.

## Структура

```text
cmd/talosdeck/       Точка входа приложения
internal/           API, клиенты инфраструктуры и подсистемы backend
web/                Vue SPA, frontend-тесты и go:embed
deploy/             Docker Compose, Helm и Kubernetes-манифесты
gitops/talosdeck/    Kustomize-манифесты для Argo CD
docs/               Пользовательская документация
Dockerfile          Сборка контейнера
Makefile            Локальная сборка и запуск
```

`bin/`, `data/`, `web/dist/` и `web/node_modules/` создаются локально и не входят
в Git. Данные подключения и секреты храните вне checkout.

Исходный код: [github.com/etosheartem/TalosDeck](https://github.com/etosheartem/TalosDeck).

Лицензия: [Apache-2.0](LICENSE). Лицензии встроенных сторонних манифестов
сохранены в `internal/k8s/addons/`.
