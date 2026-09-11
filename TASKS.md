# 📋 TalosDeck — Трекер задач и дорожная карта (Roadmap & Backlog)

> **Репозиторий**: [https://github.com/etosheartem/TalosDeck](https://github.com/etosheartem/TalosDeck)  
> **Статус проекта**: 🚀 Активная разработка Phase 2  
> **Версия**: v0.2.0-dev  
> **Последнее обновление**: 11 сентября 2026

---

## 🧭 Общий прогресс развития

| Фаза | Название | Статус | Прогресс |
| :--- | :--- | :--- | :--- |
| **Phase 1** | Базовый MVP: mTLS, Ноды, Службы, dmesg, Docker, K8s Deploy | ✅ Завершено | `[█████████████████████████] 100%` |
| **Phase 2** | Мультивкладочный UI, Storage/Disks, MachineConfig, Workloads | ✅ Завершено | `[█████████████████████████] 100%` |
| **Phase 3** | Cluster Operations: etcd Backup, Full DR Archive, Maintenance | ✅ Завершено | `[█████████████████████████] 100%` |
| **Phase 4** | Интеграция с Proxmox VE: Автосоздание ВМ и масштабирование | ✅ Завершено | `[█████████████████████████] 100%` |
| **Phase 5** | Production: Telegram-алерты, Health Watcher, JWT/RBAC, Аудит | ✅ Завершено | `[█████████████████████████] 100%` |

---

## 📦 Phase 1: MVP Core (Завершено)

- [x] **Backend**: Нативная интеграция с Talos SDK v1.14.0 (mTLS по `talosconfig`).
- [x] **Backend**: Опрос статусов нод, версий, аптайма, состояния `etcd`, `kubelet`, `containerd`, `apid`.
- [x] **Backend**: WebSocket-стриминг логов ядра (`dmesg`) и логов служб.
- [x] **Backend**: Безопасный вызов reboot нод.
- [x] **Frontend**: Vue 3 + Tailwind CSS v4 + TypeScript + Lucide icons.
- [x] **Frontend**: Двуязычность (`RU` / `EN`) с сохранением в `localStorage`.
- [x] **Frontend**: Тёмная тема (deep zinc-950, glowing status indicators).
- [x] **Frontend**: Живой терминал логов с автопрокруткой, паузой, очисткой и выгрузкой в `.log`.
- [x] **DevOps**: Multi-stage Dockerfile (Bun -> Go -> минимальный Alpine 47MB).
- [x] **DevOps**: Kubernetes манифесты (`deploy/`): Secret generator, Deployment, Service NodePort 32000.
- [x] **DevOps**: Root Makefile и руководство `DEPLOY.md`.
- [x] **Тестирование**: Проверена работа с реальным кластером (`10.42.0.110`, `10.42.0.111`, `10.42.0.112`).

---

## 🚀 Phase 2: Мультивкладочная архитектура и инспекция нод (Завершено)

> **Цель**: Разгрузить интерфейс с одной страницы на удобные тематические вкладки, добавить инспекцию дисков/хранилища, просмотр MachineConfig и обзор Kubernetes workloads.

### 2.1. UI/UX: Разделение на вкладки и боковая панель (Sidebar Navigation)
- [x] **Left Sidebar Navigation**: Боковая панель с 5 независимыми разделами:
  - 🖥️ **Ноды (Nodes)** — лаконичные карточки серверов, сводка кластера и быстрые действия (Services, Logs, Reboot).
  - 💾 **Диски и хранилище (Storage)** — физические диски, SMART, разметка разделов `/dev/sda`, монтирования.
  - 📜 **Конфигурация (MachineConfig)** — инспектор YAML-манифестов нод с подсветкой, поиском, копированием и скачиванием.
  - 📦 **Нагрузка (Workloads)** — обзор запущенных в кластере подов и пространств имён (CoreDNS, Flannel, ваши приложения).
  - ⚡ **Операции (Operations)** — здоровье etcd, статус кворума, запуск rolling-перезагрузки, перевод в maintenance mode.
- [x] **Quick Indicators & Badges**: Бейджи с числом нод, подов и пульсирующий индикатор здоровья etcd прямо в боковом меню.
- [x] **Адаптивность**: Мобильное меню со сдвигом (drawer) и плавное переключение разделов без перезагрузки страницы.
- [x] **Локализация**: Полный перевод всех разделов на русский и английский языки (`RU` / `EN`).
- [x] **GitHub Link**: В боковой панели и в футере закреплена ссылка на репозиторий `https://github.com/etosheartem/TalosDeck`.

### 2.2. Бэкенд: Новые API эндпоинты
- [x] `GET /api/nodes/:ip/disks` — опрос накопителей ноды через Talos SDK (`client.Disks` и сопоставление разделов COSI).
- [x] `GET /api/nodes/:ip/config` — получение MachineConfig ноды в JSON и сыром YAML (`?format=yaml`).
- [x] `GET /api/cluster/etcd` — опрос здоровья кворума etcd и списка членов.
- [x] `GET /api/k8s/pods` — обзор подов кластера с фильтрацией по неймспейсам и нодам.
- [x] `GET /api/k8s/namespaces` — список активных неймспейсов.

---

## ⚡ Phase 3: Кластерные операции и резервное копирование (Завершено)

- [x] **Etcd Snapshot Engine**:
  - [x] Вызов нативного Talos SDK `client.EtcdSnapshot` для Control Plane ноды.
  - [x] Вычисление контрольных сумм SHA256 и сохранение метаданных.
- [x] **Full Cluster Disaster Recovery Archive**:
  - [x] Формирование единого `.tar.gz` архива: `metadata.json`, `talosconfig`, MachineConfigs всех нод, снимок etcd.
- [x] **Backup API (`internal/api/backups.go`)**:
  - [x] `GET /api/backups` — список всех доступных резервных копий.
  - [x] `POST /api/backups/create` — создание снапшота etcd или полного бэкапа кластера.
  - [x] `GET /api/backups/:id/download` — скачивание архива из браузера.
  - [x] `DELETE /api/backups/:id` — безопасное удаление архива с метаданными.
- [x] **Режим обслуживания (Maintenance Mode)**:
  - [x] Cordon & Uncordon ноды через API.
  - [x] Мастер последовательной перезагрузки нод (Rolling Reboot).

---

## ☁️ Phase 4: Интеграция с Proxmox VE (Cloud Provider Lite) (Завершено)

- [x] **Подключение к Proxmox VE API**:
  - [x] Настройка подключения к `192.168.88.169:8006` по API-токену и тикетам (Username/Password).
  - [x] Чтение ресурсов сервера Proxmox (`GetNodeStatus`: CPU, свободная RAM, емкость пула `local-lvm`).
  - [x] Автоматическое определение следующего свободного идентификатора ВМ (`GetNextVMID`).
- [x] **API управления воркерами Proxmox**:
  - [x] Создание ВМ воркера (`CreateTalosWorker`: 2 vCPU, 3GB RAM, 30GB `local-lvm`, `vmbr0`, `data:iso/talos-v1.14.0-qemu-guest-agent.iso`, QEMU agent).
  - [x] Остановка и удаление ВМ (`DeleteWorker`: graceful stop + purge storage disks).
  - [x] Эндпоинты Fiber API: `GET /api/proxmox/status`, `POST /api/proxmox/worker`, `DELETE /api/proxmox/worker/:vmid`, `GET /api/proxmox/next-vmid`.
- [x] **Мастер добавления воркера в Web UI (Scale-Out Wizard)**:
  - [x] Карточка статуса ресурсов Proxmox VE в `NodesView.vue` (хост, CPU, доступная RAM, емкость дисков).
  - [x] Диалоговое окно быстрого масштабирования нод кластера (`AddWorkerModal.vue`).
  - [x] Автоподстановка свободного VMID, валидация полей, 4-шаговый интерактивный таймлайн создания ВМ.

---

## 🛡️ Phase 5: Безопасность, аудит и алертинг (Завершено)

- [x] **Аутентификация и RBAC**:
  - [x] Вход по паролю администратора (по умолчанию `admin` или `TALOSDECK_ADMIN_PASSWORD`).
  - [x] JWT-сессии (HMAC-SHA256, 24 часа), эндпоинты `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/me`.
  - [x] Защита чувствительных методов (`RequireAuth`: reboot, backup create/delete, worker create/delete).
  - [x] Модальный диалог входа [`LoginModal.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/LoginModal.vue) и переключение ролей `Admin` / `Viewer` в шапке `TopBar.vue`.
- [x] **Аудит и логирование действий (Audit Trail)**:
  - [x] Пакет `internal/audit`: кольцевой буфер на 1000 событий в памяти + сохранение в `data/audit.log` (JSON Lines).
  - [x] Фиксация всех ключевых действий: `auth.login`, `auth.logout`, `node.reboot`, `backup.create`, `backup.delete`, `worker.create`, `worker.delete`.
  - [x] Эндпоинт `GET /api/audit` с фильтрами по действию и полнотекстовым поиском.
  - [x] Интерактивная таблица логов аудита в `OperationsView.vue` с поиском, фильтрами и бейджами статусов.
- [x] **Telegram Алертинг и мониторинг здоровья кластера**:
  - [x] Пакет `internal/alerts`: фоновый наблюдатель `Watcher` с дебаунсингом и кольцевой буфер последних событий.
  - [x] Фильтрация по минимальному уровню (`INFO`, `WARNING`, `CRITICAL`).
  - [x] Эндпоинты `GET /api/alerts/config`, `POST /api/alerts/config`, `POST /api/alerts/test`, `GET /api/alerts/history`.
  - [x] Панель настройки Telegram в `OperationsView.vue`: ввод токена, чата, порога, кнопка моментального теста и журнал недавних оповещений.

