<div align="center">

# ⚡ TalosDeck

### Modern, Open-Source, Self-Hosted Control Plane & Web UI for Talos Linux

[![License: MIT](https://img.shields.io/badge/License-MIT-emerald.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.27+-cyan.svg)](https://golang.org)
[![Vue](https://img.shields.io/badge/Vue-3.x-emerald.svg)](https://vuejs.org)
[![Tailwind](https://img.shields.io/badge/Tailwind-v4-blue.svg)](https://tailwindcss.com)
[![Talos Linux](https://img.shields.io/badge/Talos_Linux-v1.14.0-violet.svg)](https://www.talos.dev)

**TalosDeck** — это легковесный, полнофункциональный open-source веб-интерфейс для мониторинга, обслуживания и управления кластерами **Talos Linux Kubernetes**. Альтернатива проприетарному SaaS Omni, работающая автономно в вашем контуре.

[🇷🇺 Документация на русском](#-основные-возможности) • [🇬🇧 English Docs](#-key-features) • [🚀 Быстрый старт](#-быстрый-старт) • [📦 Развёртывание в K8s](#-развёртывание-в-kubernetes)

</div>

---

## 🌟 Основные возможности

1. **Мониторинг кластера и нод**:
   - Живой статус нод, аптайм, версии Talos и Kubernetes.
   - Статусы ключевых компонентов: `etcd`, `kubelet`, `containerd`, `apid`.
2. **Боковая навигация и модульный интерфейс**:
   - 🖥️ **Ноды (Nodes)**: Сводка нод, роли (Control Plane / Worker), быстрые действия (Services, Logs, Reboot), виджет ресурсов Proxmox VE.
   - 💾 **Хранилище (Storage)**: Физические накопители (`/dev/sda`), размеры, разделы COSI (`/system/state`, `/var`, CSI volumes).
   - 📜 **Конфигурация (MachineConfig)**: Инспектор активных YAML-манифестов нод с поиском и скачиванием.
   - 📦 **Нагрузка (Workloads)**: Список Kubernetes-подов по всем пространствам имён с фильтрами и статусами.
   - ⚡ **Операции (Operations)**: Проверка кворума etcd, Rolling Reboot, Maintenance Mode, резервное копирование, Telegram-алертинг и Журнал аудита.
3. **Мастер масштабирования воркеров в Proxmox VE (Scale-Out Wizard)**:
   - Интерактивное модальное окно создания новых воркеров в Proxmox VE в один клик.
   - Автоматический подбор свободного VMID, настройка vCPU, RAM, диска `local-lvm`, сетевого моста `vmbr0` и 4-шаговый визуализатор процесса.
4. **Безопасность и RBAC (JWT Auth)**:
   - Авторизация администратора по JWT-токенам (пароль `admin` по умолчанию или из `TALOSDECK_ADMIN_PASSWORD`).
   - Разделение прав: безопасный режим просмотра (Viewer) и режим управления (Admin) с защитой деструктивных действий (Reboot, Backup, Scale-Out).
5. **Журнал аудита действий (Audit Trail)**:
   - Потокобезопасная фиксация всех операций (`internal/audit`): авторизация, перезагрузка, создание бэкапов, добавление воркеров.
   - Фильтрация по типам действий, поиск по журналу и выгрузка логов.
6. **Telegram-алертинг и мониторинг здоровья**:
   - Фоновый наблюдатель (`Watcher`) с умным дебаунсингом (оповещения при смене `Ready` ↔ `NotReady` или падении кворума etcd).
   - Уровни оповещений (`INFO`, `WARNING`, `CRITICAL`), проверка связи и моментальная отправка тестового уведомления прямо из UI.
7. **Резервное копирование и DR (Disaster Recovery)**:
   - Снятие консистентных снапшотов etcd через нативный Talos SDK.
   - Формирование полного `.tar.gz` архива для восстановления (etcd + talosconfig + machine-configs + метаданные).
8. **Живой стриминг логов (WebSocket)**:
   - Стриминг сообщений ядра `dmesg` и логов служб (`kubelet`) с автоскроллом, паузой и выгрузкой в `.log`.
9. **Двуязычность и тёмная тема**:
   - Полная поддержка языков **RU / EN** с сохранением в `localStorage`.
   - Глубокая тёмная палитра `zinc-950` с неоновыми статусными акцентами.

---

## 🏗️ Архитектура

```
+-------------------------------------------------------------------------------+
|                             Браузер (Пользователь)                            |
|             Vue 3 + Vite + Tailwind CSS v4 + TypeScript + Lucide              |
+---------------------------------------+---------------------------------------+
                                        | REST API & WebSockets (:8080)
+---------------------------------------v---------------------------------------+
|                       TalosDeck Engine (Единый бинарник Go)                   |
|                                                                               |
|  [REST/WS Handlers] <---> [Talos Manager (SDK)] <---> [K8s Client-Go]        |
|  [Proxmox Client]   <---> [Backup Manager]      <---> [Telegram Watcher]      |
+---------------------------------------+---------------------------------------+
                                        | gRPC / mTLS (порт 50000)
        +-------------------------------+-------------------------------+
        |                                                               |
+-------v------------------+                                +-----------v-----------+
|  talos-cp-1 (10.42.0.110)|                                |  talos-workers (111/112)|
|  - etcd, apiserver       |                                |  - kubelet, containerd|
|  - apid (:50000)         |                                |  - apid (:50000)      |
+--------------------------+                                +-----------------------+
```

---

## 🚀 Быстрый старт

### Требования
- Go 1.23+
- Bun или Node.js (для сборки фронтенда)
- Файл `talosconfig` с правами администратора кластера

### Запуск локально:
```bash
# Клонирование репозитория
git clone https://github.com/etosheartem/TalosDeck.git
cd TalosDeck

# Сборка и запуск
make build
./bin/talosdeck --talosconfig=../cluster-config/talosconfig
```
Откройте в браузере: **`http://localhost:8080`**

---

## 📦 Развёртывание в Kubernetes

```bash
# 1. Создание секрета с конфигурацией подключения
./deploy/secret-create.sh

# 2. Сборка Docker-образа
make docker-build

# 3. Применение манифестов
kubectl apply -f deploy/deployment.yaml -f deploy/service.yaml
```

Интерфейс будет доступен на любой ноде кластера:  
`http://<IP-любой-ноды>:32000`

---

## 📜 API Эндпоинты

| Метод | Эндпоинт | Описание |
| :--- | :--- | :--- |
| `GET` | `/api/cluster` | Сводка состояния кластера и версии |
| `GET` | `/api/cluster/etcd` | Статус кворума etcd и список участников |
| `GET` | `/api/nodes` | Список нод с ролями и статусами служб |
| `GET` | `/api/nodes/:ip/disks` | Накопители и разделы COSI ноды |
| `GET` | `/api/nodes/:ip/config` | MachineConfig ноды в JSON или YAML (`?format=yaml`) |
| `POST` | `/api/nodes/:ip/reboot` | Безопасная перезагрузка ноды |
| `GET` | `/api/k8s/pods` | Список подов Kubernetes с фильтрацией по ns |
| `GET` | `/api/backups` | Список резервных копий и снимков etcd |
| `POST` | `/api/backups/create` | Создание снимка etcd или полного DR-бэкапа |
| `GET` | `/api/proxmox/status` | Ресурсы хоста Proxmox VE (RAM, CPU, `local-lvm`) |
| `POST` | `/api/proxmox/worker` | Автосоздание новой ноды-воркера в Proxmox |
| `GET` | `/api/alerts/config` | Состояние и настройки Telegram-алертинга |
| `WS` | `/ws/nodes/:ip/dmesg` | Живой поток логов ядра (dmesg) через WebSocket |

---

## 📄 Лицензия

Распространяется под лицензией **MIT**. Подробности в файле `LICENSE`.
Репозиторий проекта: [https://github.com/etosheartem/TalosDeck](https://github.com/etosheartem/TalosDeck)
