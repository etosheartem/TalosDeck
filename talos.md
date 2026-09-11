# Проект: Open-Source Self-Hosted Talos UI / Control Plane (Альтернатива Omni)

> **Рабочее название**: `TalosDeck` (или `TalosForge`)  
> **Миссия**: Создать легковесный, полностью бесплатный, self-hosted веб-интерфейс для мониторинга и управления нодами и кластерами Talos Linux без привязки к проприетарному SaaS Omni.

---

## 1. Проблема и ценность продукта

| Параметр | Sidero Omni | TalosDeck (Наш MVP) |
| :--- | :--- | :--- |
| **Лицензия** | Проприетарная, платная для коммерции | 100% Open Source (MIT / Apache 2.0) |
| **Хостинг** | Преимущественно Cloud SaaS / сложный On-Prem | 1 бинарник / Docker-контейнер у вас в сети |
| **Приватность** | Телеметрия и зависимость от внешнего контура | Полностью изолированный, air-gapped ready |
| **Порог входа** | Регистрация, токены, облачные аккаунты | Скачал, подсунул `talosconfig`, нажал запуск |

---

## 2. Архитектура системы

```
+----------------------------------------------------------------------+
|                           Пользователь (Браузер)                     |
|                   Vue 3 / React + Tailwind CSS + Lucide              |
+-----------------------------------+----------------------------------+
                                    | HTTP REST (Команды)
                                    | WebSockets (Стриминг логов/метрик)
+-----------------------------------v----------------------------------+
|                      Бэкенд TalosDeck (Go / Golang)                  |
|                                                                      |
|  [REST API Router]  <--->  [Talos Client Service]  <---> [Config]    |
|                             (siderolabs/talos SDK)     (talosconfig) |
+-----------------------------------+----------------------------------+
                                    |
                                    | gRPC поверх mTLS (порт 50000)
       +----------------------------+----------------------------+
       |                                                         |
+------v-------------------+      +------------------------------v----+
|  talos-cp-1 (Master)     |      |  talos-worker-1 / worker-2        |
|  - etcd, apiserver       |      |  - kubelet, containerd, workloads |
|  - talos apid (:50000)   |      |  - talos apid (:50000)            |
+--------------------------+      +-----------------------------------+
```

### Ключевые преимущества архитектуры:
- **Нет агентов внутри нод**: Talos уже имеет встроенный демон `apid`, слушающий порт `50000`.
- **Строгая типизация**: официальный Go SDK Talos исключает необходимость парсить текст или запускать CLI-команды в subshell.

---

## 3. Стек технологий MVP

- **Backend**: **Go 1.23+**
  - Официальный SDK Talos: `github.com/siderolabs/talos/pkg/machinery/client`
  - HTTP & WebSocket сервер: `github.com/gofiber/fiber/v2` (или `chi` + `gorilla/websocket`)
- **Frontend**: **Vue 3 + Vite + Tailwind CSS** (или React + Shadcn UI)
  - Графики метрик: Chart.js / Recharts
  - Терминал логов: xterm.js (для красивого ANSI-вывода `dmesg` и логов служб)
- **Упаковка**: единый статический бинарник (фронтенд вшивается в Go через `embed.FS`).

---

## 4. Структура репозитория

```text
talosdeck/
├── cmd/
│   └── talosdeck/
│       └── main.go              # Точка входа, запуск сервера
├── internal/
│   ├── api/                     # HTTP и WebSocket хендлеры
│   │   ├── nodes.go
│   │   ├── services.go
│   │   ├── logs.go
│   │   └── cluster.go
│   └── talos/                   # Обёртка над Talos Go SDK
│       ├── client.go            # Инициализация mTLS клиента
│       ├── node_ops.go          # Перезагрузка, статус, версия
│       ├── metrics.go           # Сбор ресурсов CPU/RAM
│       └── streamer.go          # Стриминг dmesg и логов
├── web/                         # SPA фронтенд (Vue/React)
│   ├── src/
│   │   ├── views/
│   │   │   ├── Dashboard.vue
│   │   │   ├── NodeDetails.vue
│   │   │   └── ConfigEditor.vue
│   │   └── App.vue
│   ├── package.json
│   └── vite.config.ts
├── go.mod
├── go.sum
└── Dockerfile                   # Multi-stage сборка в scratch/alpine
```

---

## 5. Рабочий прототип бэкенда на Go (Core Engine)

Ниже приведён готовый каркас на Go, демонстрирующий прямое взаимодействие с вашим кластером через `talosconfig`:

### `go.mod`
```go
module talosdeck

go 1.23

require (
    github.com/gofiber/fiber/v2 v2.52.5
    github.com/gofiber/websocket/v2 v2.2.1
    github.com/siderolabs/talos/pkg/machinery v1.9.0
)
```

### `internal/talos/client.go`
```go
package talos

import (
    "context"
    "fmt"
    "os"

    "github.com/siderolabs/talos/pkg/machinery/client"
    "github.com/siderolabs/talos/pkg/machinery/client/config"
)

type TalosManager struct {
    client *client.Client
    cfg    *config.Config
}

// NewTalosManager загружает talosconfig и создаёт mTLS клиент
func NewTalosManager(talosconfigPath string) (*TalosManager, error) {
    data, err := os.ReadFile(talosconfigPath)
    if err != nil {
        return nil, fmt.Errorf("ошибка чтения talosconfig: %w", err)
    }

    cfg, err := config.Open(data)
    if err != nil {
        return nil, fmt.Errorf("ошибка парсинга talosconfig: %w", err)
    }

    ctx := context.Background()
    c, err := client.New(ctx, client.WithConfig(cfg))
    if err != nil {
        return nil, fmt.Errorf("ошибка создания Talos клиента: %w", err)
    }

    return &TalosManager{
        client: c,
        cfg:    cfg,
    }, nil
}
```

### `internal/talos/node_ops.go`
```go
package talos

import (
    "context"
    "time"

    "github.com/siderolabs/talos/pkg/machinery/client"
)

type NodeOverview struct {
    IP       string `json:"ip"`
    Hostname string `json:"hostname"`
    Version  string `json:"version"`
    Ready    bool   `json:"ready"`
}

// GetNodeVersion опрашивает версию и состояние ноды по IP
func (m *TalosManager) GetNodeVersion(ctx context.Context, nodeIP string) (*NodeOverview, error) {
    nodeCtx := client.WithNodes(ctx, nodeIP)
    
    resp, err := m.client.Version(nodeCtx)
    if err != nil {
        return &NodeOverview{IP: nodeIP, Ready: false}, err
    }

    versionInfo := resp.GetMessages()[0].GetVersion()

    return &NodeOverview{
        IP:       nodeIP,
        Hostname: versionInfo.GetHostname(),
        Version:  versionInfo.GetTag(),
        Ready:    true,
    }, nil
}

// RebootNode отправляет команду безопасного перезапуска
func (m *TalosManager) RebootNode(ctx context.Context, nodeIP string) error {
    nodeCtx := client.WithNodes(ctx, nodeIP)
    return m.client.Reboot(nodeCtx)
}

// BootstrapCluster выполняет первичную инициализацию etcd
func (m *TalosManager) BootstrapCluster(ctx context.Context, controlPlaneIP string) error {
    nodeCtx := client.WithNodes(ctx, controlPlaneIP)
    return m.client.Bootstrap(nodeCtx)
}
```

### `internal/talos/streamer.go` (Стриминг логов ядра dmesg в WebSocket)
```go
package talos

import (
    "context"

    "github.com/siderolabs/talos/pkg/machinery/api/machine"
    "github.com/siderolabs/talos/pkg/machinery/client"
)

// StreamDmesg передает поток сообщений ядра dmesg в канал
func (m *TalosManager) StreamDmesg(ctx context.Context, nodeIP string, logChan chan<- string) error {
    nodeCtx := client.WithNodes(ctx, nodeIP)
    
    stream, err := m.client.Dmesg(nodeCtx, client.WithTailLines(100), client.WithFollow(true))
    if err != nil {
        return err
    }

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
            msg, err := stream.Recv()
            if err != nil {
                return err
            }
            logChan <- msg.GetMsg()
        }
    }
}
```

### `cmd/talosdeck/main.go`
```go
package main

import (
    "flag"
    "log"

    "github.com/gofiber/fiber/v2"
    "github.com/gofiber/fiber/v2/middleware/cors"
    "github.com/gofiber/websocket/v2"

    "talosdeck/internal/talos"
)

func main() {
    configPath := flag.String("talosconfig", "./cluster-config/talosconfig", "Путь к talosconfig")
    port := flag.String("port", ":8080", "Порт веб-сервера")
    flag.Parse()

    manager, err := talos.NewTalosManager(*configPath)
    if err != nil {
        log.Fatalf("Не удалось подключиться к Talos: %v", err)
    }

    app := fiber.New(fiber.Config{
        AppName: "TalosDeck MVP v0.1",
    })

    app.Use(cors.New())

    // Список нод кластера
    app.Get("/api/nodes", func(c *fiber.Ctx) error {
        // В реальном MVP читаем endpoints из talosconfig
        nodes := []string{"10.42.0.110", "10.42.0.111", "10.42.0.112"}
        var results []*talos.NodeOverview

        for _, ip := range nodes {
            info, _ := manager.GetNodeVersion(c.Context(), ip)
            results = append(results, info)
        }
        return c.JSON(results)
    })

    // Перезагрузка ноды
    app.Post("/api/nodes/:ip/reboot", func(c *fiber.Ctx) error {
        ip := c.Params("ip")
        if err := manager.RebootNode(c.Context(), ip); err != nil {
            return c.Status(500).JSON(fiber.Map{"error": err.Error()})
        }
        return c.JSON(fiber.Map{"status": "rebooting", "node": ip})
    })

    // WebSocket стриминг логов ядра (dmesg)
    app.Get("/ws/nodes/:ip/dmesg", websocket.New(func(c *websocket.Conn) {
        ip := c.Params("ip")
        logChan := make(chan string, 50)

        go func() {
            _ = manager.StreamDmesg(c.Context(), ip, logChan)
        }()

        for line := range logChan {
            if err := c.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
                break
            }
        }
    }))

    log.Printf("🚀 TalosDeck запущен на http://localhost%s", *port)
    log.Fatal(app.Listen(*port))
}
```

---

## 6. Спецификация REST & WebSocket API

| Метод | Эндпоинт | Назначение |
| :--- | :--- | :--- |
| `GET` | `/api/nodes` | Список нод с версией, аптаймом и статусом готовности |
| `GET` | `/api/nodes/{ip}/services` | Состояние внутренних служб (`etcd`, `kubelet`, `containerd`) |
| `GET` | `/api/nodes/{ip}/disks` | Список физических дисков и разметки разделов |
| `POST` | `/api/nodes/{ip}/reboot` | Безопасная перезагрузка ноды |
| `POST` | `/api/nodes/{ip}/upgrade` | Обновление ОС ноды на указанный контейнерный тег Talos |
| `POST` | `/api/cluster/bootstrap` | Инициализация кластера (bootstrap etcd) |
| `WS` | `/ws/nodes/{ip}/dmesg` | Живой поток логов ядра (dmesg) |
| `WS` | `/ws/nodes/{ip}/logs/{svc}` | Живой поток логов конкретной службы (`kubelet` и др.) |

---

## 7. Концепт интерфейса (Wireframe)

### Главный экран (Cluster Dashboard)
```text
+-------------------------------------------------------------------------------+
|  [⚡ TalosDeck]   Cluster: lab-k8s (Talos v1.14.0 / k8s v1.37.0)   [+ Add Node]|
+-------------------------------------------------------------------------------+
|                                                                               |
|  NODES (3)                                                                    |
|  +-----------------------------+  +----------------------------------------+  |
|  | talos-cp-1                  |  | talos-worker-1                         |  |
|  | Role: Control Plane         |  | Role: Worker                           |  |
|  | IP: 10.42.0.110    [Ready]  |  | IP: 10.42.0.111               [Ready]  |  |
|  | CPU: 12%  RAM: 1.8/4.0 GB   |  | CPU: 5%   RAM: 0.9/3.0 GB              |  |
|  | etcd: Healthy | apid: OK    |  | kubelet: Healthy | containerd: OK      |  |
|  | [Logs] [Services] [Reboot]  |  | [Logs] [Services] [Reboot]             |  |
|  +-----------------------------+  +----------------------------------------+  |
|  +-----------------------------+                                              |
|  | talos-worker-2              |                                              |
|  | Role: Worker                |                                              |
|  | IP: 10.42.0.112    [Ready]  |                                              |
|  | CPU: 7%   RAM: 1.1/3.0 GB   |                                              |
|  | kubelet: Healthy | crictl:OK|                                              |
|  | [Logs] [Services] [Reboot]  |                                              |
|  +-----------------------------+                                              |
|                                                                               |
|  LIVE LOGS CONSOLE (talos-cp-1)                                 [Pause] [Clear]|
|  > [ 102.43] talos: system service etcd is running                            |
|  > [ 103.11] kubelet: pod network initialized (flannel)                      |
|  > [ 105.02] k8s: node talos-cp-1 became Ready                                |
+-------------------------------------------------------------------------------+
```

---

## 8. Пошаговый план реализации

1. **День 1-2**: Инициализация Go-модуля, подключение пакета `github.com/siderolabs/talos/pkg/machinery/client`. Тест чтения версии и служб с ноды `10.42.0.110`.
2. **День 3**: Реализация WebSocket стриминга `dmesg` и вызова `Reboot`.
3. **День 4-5**: Быстрый фронтенд на Vue 3 + Tailwind CSS: карточки нод, бейджи статусов, кнопка перезагрузки с подтверждением, окно логов с xterm.js.
4. **День 6**: Упаковка в single-binary с помощью Go `embed` (фронтенд отдаётся самим Go-сервером).
5. **День 7**: Создание красивого `README.md`, публикация на GitHub и пост в Reddit `r/taloslinux` / `r/homelab`.
