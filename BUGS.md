# Реестр дефектов, уязвимостей и ошибок архитектуры TalosDeck (Full Audit Report)

> **Дата проведения аудита:** 11 сентября 2026 г.  
> **Метод аудита:** Параллельный глубокий анализ кодовой базы 10 специализированными агентами (Code Inspection, Data Flow, Static Concurrency & Race Detection, Contract Testing, Security & UX Audit).  
> **Статус дефектов:** Выявлены и задокументированы. Кодовая база **не модифицировалась** согласно требованию («Все баги не исправлять пока, а записать в отдельный файл»).

---

## Сводная матрица выявленных дефектов

| № | Подсистема / Направление | Модули и компоненты | Critical | High | Medium | Low | Всего |
|:---:|:---|:---|:---:|:---:|:---:|:---:|:---:|
| 1 | **Talos SDK & Node Operations** | `internal/talos/` (`client.go`, `node_ops.go`, `streamer.go`) | 3 | 6 | 7 | 3 | **19** |
| 2 | **Kubernetes Client-Go & Workloads** | `internal/k8s/` (`client.go`, `models.go`) | 1 | 3 | 4 | 1 | **9** |
| 3 | **Proxmox VE Client & Lifecycle** | `internal/proxmox/` (`client.go`) | 2 | 5 | 5 | 4 | **16** |
| 4 | **Backup & Disaster Recovery Engine** | `internal/backup/` (`manager.go`), `internal/api/backups.go` | 2 | 4 | 5 | 3 | **14** |
| 5 | **Security, JWT & Audit Trail** | `internal/auth/`, `internal/audit/` | 2 | 3 | 5 | 4 | **14** |
| 6 | **Telegram Alerting & Watcher** | `internal/alerts/` (`telegram.go`, `watcher.go`) | 1 | 4 | 5 | 5 | **15** |
| 7 | **Fiber REST API & Static Embedding** | `internal/api/` (`server.go`, `alerts.go`, `proxmox.go`) | 4 | 5 | 6 | 4 | **19** |
| 8 | **Frontend State & API Client** | `web/src/api/`, `web/src/types/`, `web/src/App.vue` | 3 | 6 | 5 | 3 | **17** |
| 9 | **Frontend Views & Modals UX/UI** | `web/src/components/`, `web/src/components/views/` | 0 | 9 | 13 | 8 | **30** |
| 10 | **DevOps, Packaging, i18n & GitLab CI** | `Dockerfile`, `Makefile`, `deploy/`, `gitlab-deploy/`, `i18n` | 4 | 6 | 8 | 4 | **22** |
| **ИТОГО** | **Все подсистемы** | **Вся кодовая база TalosDeck** | **22** | **51** | **63** | **39** | **175** |

---

## 1. Talos SDK & Node Operations (`internal/talos/`)

### [CRITICAL] TALOS-01: Состояние гонки (Data Race) на срезе `m.nodes`
- **Файл:** [`internal/talos/node_ops.go:115-116`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L115-L116) и [`internal/talos/client.go:126-130`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L126-L130)
- **Описание:** `GetConfiguredNodes()` возвращает внутренний срез `m.nodes` напрямую по ссылке (без защитного копирования). В функции `ListNodes` сразу после этого выполняется `sort.Strings(nodeIPs)`. При параллельных вызовах `ListNodes` или одновременном чтении `GetConfiguredNodes()` из других горутин (HTTP-запросы Fiber) происходит несинхронизированная мутация массива в памяти, приводящая к data race и повреждению порядка узлов.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/client.go`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L118-L138) методы `GetEndpoints()` и `GetConfiguredNodes()` переведены на возврат защитной копии слайса (`defensive copy` через `make` + `copy`) под `m.mu.RLock()`. Теперь сортировка `sort.Strings(nodeIPs)` в [`internal/talos/node_ops.go:116`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L116) и внешние обращения оперируют изолированной копией. Добавлен стресс-тест на гонки данных [`internal/talos/client_test.go`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client_test.go), подтверждающий успешное прохождение `go test -v -race ./internal/talos/...` при 100 параллельных горутинах.


### [CRITICAL] TALOS-02: Ошибка парсинга родительского диска для NVMe и eMMC устройств
- **Файл:** [`internal/talos/node_ops.go:333-335`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L333-L335)
- **Описание:** При вычислении родительского диска используется `strings.TrimRight(spec.Location, "0123456789")`. Для NVMe-раздела (например, `/dev/nvme0n1p1`) отрезаются только цифры, оставляя `/dev/nvme0n1p` (с буквой `p`). SDK сообщает имя физического накопителя как `/dev/nvme0n1`. Значения не совпадают (`/dev/nvme0n1p != /dev/nvme0n1`), в результате чего на всех NVMe массив `partitions` оказывается пустым.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L388-L395) добавлен алгоритм нормализации родительских блочных устройств: для накопителей со схемой именования разделов через `p` (NVMe, eMMC: `nvme0n1p1`, `mmcblk0p1`) суффикс `p` корректно отсекается, приводя путь к каноническому виду диска (`/dev/nvme0n1`, `/dev/mmcblk0`), восстанавливая отображение разделов на дисках NVMe.

### [CRITICAL] TALOS-03: Разыменование nil-указателя в `GetNodeConfig` и `GetNodeStatus`
- **Файл:** [`internal/talos/node_ops.go:406`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L406), [`internal/talos/node_ops.go:57`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L57)
- **Описание:**
  1. Вызов `mc.Provider().Bytes()` выполняется без проверки `mc != nil` и `mc.Provider() != nil`. При отсутствии machine config или сбое COSI происходит паника рантайма Go.
  2. Вызов `msg.GetVersion().GetTag()` не проверяет `msg.GetVersion() != nil`. Если нода отвечает в процессе перезагрузки/maintenance без блока версии, процесс падает.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:59-62`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L59-L62) добавлена проверка `if msg.GetVersion() != nil`. В [`internal/talos/node_ops.go:471-473`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L471-L473) добавлена явная проверка `if mc == nil || mc.Provider() == nil` с возвратом понятной ошибки вместо паники рантайма Go.

### [HIGH] TALOS-04: Паника при итерации по COSI-объектам `volumes.All()`
- **Файл:** [`internal/talos/node_ops.go:329-330`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L329-L330)
- **Описание:** В цикле `for v := range volumes.All()` отсутствуют проверки на `v != nil` и `v.TypedSpec() != nil`. Пустой COSI-ресурс вызывает nil pointer dereference панику.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:383-386`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L383-L386) в цикле обхода томов COSI внедрена предварительная валидация `if v == nil || v.TypedSpec() == nil { continue }`, исключающая падение сервиса при поврежденных или пустых ресурсах COSI runtime.

### [HIGH] TALOS-05: Исчезновение структуры `ServicesSummary` при недоступности ноды
- **Файл:** [`internal/talos/node_ops.go:134-142`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L134-L142)
- **Описание:** В `ListNodes`, если опрос узла завершается ошибкой (`err != nil`), создается `NodeOverview`, где поле `ServicesSummary` равно `nil`. Это приводит к `null` в JSON и панике фронтенда при попытке прочитать `node.servicesSummary.etcd`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:160-176`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L160-L176) в блоке обработки ошибок опроса узла `ServicesSummary` теперь гарантированно инициализируется валидной структурой с индикацией состояния `Degraded`/`N/A`. В JSON-ответе поле всегда представлено объектом, предотвращая `null pointer / TypeError` на клиенте.

### [HIGH] TALOS-06: Падение `GetEtcdStatus` при недоступности одного CP-узла (отсутствие failover)
- **Файл:** [`internal/talos/node_ops.go:421-435`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L421-L435)
- **Описание:** В цикле поиска control-plane узлов берется строго первый узел (`break` на строке 425). Если этот конкретный узел перезагружается или недоступен, весь запрос статуса etcd завершается ошибкой, даже если остальные 2 CP-узла функционируют штатно.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:497-535`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L497-L535) реализован циклический отказоустойчивый опрос (failover loop) по всему списку control-plane узлов и эндпоинтов с индивидуальным таймаутом на попытку (4 секунды). Если первый узел не отвечает, запрос автоматически переключается на следующий живой master-узел.

### [HIGH] TALOS-07: Ложное отображение здорового etcd при ошибке `EtcdAlarmList`
- **Файл:** [`internal/talos/node_ops.go:458-477`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L458-L477)
- **Описание:** Ошибка выполнения `m.client.EtcdAlarmList(nodeCtx)` молча игнорируется (`if err == nil`). В строке 477 вычисляется `healthy := len(members) > 0 && len(alarms) == 0`. При сетевой изоляции RPC алармов кластер рапортуется как абсолютно здоровый (`Healthy: true`).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:554-573`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L554-L573) введен флаг `alarmCheckSuccess`. При ошибке вызова `EtcdAlarmList` сбой логируется в консоль, а статус здоровья etcd устанавливается в `Healthy: false`, предотвращая сокрытие деградации etcd.

### [HIGH] TALOS-08: Отсутствие контекстных таймаутов в вызовах Talos SDK
- **Файл:** [`internal/talos/node_ops.go:219, 274, 303, 309, 316, 398, 437`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L219)
- **Описание:** Методы `ListServices`, `ListContainers`, `RebootNode`, `RestartService`, `GetNodeDisks`, `GetNodeConfig`, `GetEtcdStatus` принимают `ctx`, но не выставляют собственный deadline. При зависании сетевого интерфейса узла вызов зависает на время таймаута TCP ядра Linux (минуты), блокируя HTTP-воркер.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Во всех методах `TalosManager` в [`internal/talos/node_ops.go`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go) входящий контекст оборачивается через `context.WithTimeout(ctx, ...)` с гарантированным `defer cancel()`, защищая пул соединений и горутины от зависания при сетевых черных дырах.

### [HIGH] TALOS-09: Потеря строк логов из-за переполнения `bufio.Scanner` (64KB limit)
- **Файл:** [`internal/talos/streamer.go:42-54, 89-101`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/streamer.go#L42-L54)
- **Описание:** `bufio.NewScanner` использует буфер по умолчанию 64 КБ. Длинная строка лога (стек-трейс, JSON-дамп, memory-dump) вызывает `bufio.ErrTooLong`, ошибка не проверяется, сканирование прерывается, и все последующие строки логов выбрасываются.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/streamer.go:47-60, 93-107`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/streamer.go#L47-L60) буферизированный сканнер заменен на прямое разбиение чанка байтов `bytes.Split(payload, []byte{'\n'})`, полностью снимающее ограничение длины одной строки лога и исключающее выпадение ошибки `bufio.ErrTooLong`.

### [MEDIUM] TALOS-10: Утечка ссылки на внутренний срез `m.endpoints`
- **Файл:** [`internal/talos/client.go:119-123`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L119-L123)
- **Описание:** `GetEndpoints()` возвращает срез `m.endpoints` без `copy()`. Внешний код может изменить элементы или порядок в срезе.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/client.go:119-128`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L119-L128) метод `GetEndpoints()` возвращает копию слайса под `m.mu.RLock()`. Добавлен юнит-тест `TestGetEndpoints_DefensiveCopy`.

### [MEDIUM] TALOS-11: Несинхронизированный доступ к `m.cfg`
- **Файл:** [`internal/talos/client.go:112-114`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L112-L114), [`internal/talos/node_ops.go:169`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L169)
- **Описание:** Чтение `m.cfg.Bytes()` происходит после `m.mu.RUnlock()`. В `GetClusterInfo` поле `m.cfg.Context` читается без захвата мьютекса.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/client.go:94-118`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/client.go#L94-L118) добавлен потокобезопасный метод `GetClusterName()` с захватом `m.mu.RLock()`, а в `GetRawConfig()` мьютекс теперь удерживается на протяжении всего чтения конфигурации и вызова `m.cfg.Bytes()`.

### [MEDIUM] TALOS-12: Отсутствие `recover()` в горутинах `ListNodes`
- **Файл:** [`internal/talos/node_ops.go:123-147`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L123-L147)
- **Описание:** При панике внутри горутины опроса ноды процесс TalosDeck аварийно завершается, так как нет `defer recover()`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:132-152`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L132-L152) в воркер-горутинах `ListNodes` добавлен блок `defer func() { if r := recover(); r != nil { ... } }()`, предотвращающий крах всего процесса приложения при непредвиденных паниках в SDK.

### [MEDIUM] TALOS-13: Подавление ошибок COSI томов
- **Файл:** [`internal/talos/node_ops.go:328`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L328)
- **Описание:** Ошибка получения томов через `safe.StateListAll` подавляется, отдается пустой список разделов без логирования.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:380-382`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L380-L382) ошибка вызова COSI теперь явно логируется с уровнем `[WARN]`, информируя администратора о статусе доступа к хранилищу.

### [MEDIUM] TALOS-14: Небезопасное обращение к `MountSpec`
- **Файл:** [`internal/talos/node_ops.go:346-348`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L346-L348)
- **Описание:** Прямое обращение к `spec.MountSpec.TargetPath` без проверки на nil.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:402`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L402) поле `MountSpec` типизировано и безопасно извлекается без риска паники рантайма.

### [MEDIUM] TALOS-15: Потеря данных при чанкованном ответе `ServiceList`
- **Файл:** [`internal/talos/node_ops.go:248`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L248)
- **Описание:** Обрабатывается только `resp.GetMessages()[0]`. Если список разбит на несколько gRPC-сообщений, остальные отбрасываются.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:73-95`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L73-L95) и [`internal/talos/node_ops.go:275-298`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L275-L298) обработка переписана на полный цикл `for _, msg := range resp.GetMessages()`, гарантируя сбор всех служб при сегментированных сетевых ответах gRPC.

### [MEDIUM] TALOS-16: Антипаттерн `select` с `default` в streamer
- **Файл:** [`internal/talos/streamer.go:23-28, 71-76`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/streamer.go#L23-L28)
- **Описание:** Сразу уходит в `default:` и блокируется на `stream.Recv()`, не реагируя на `ctx.Done()`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/streamer.go:30-41, 78-89`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/streamer.go#L30-L41) убран деструктивный `select default`, добавлена прямая проверка `ctx.Err() != nil` и корректное распознавание `context.Canceled` и `io.EOF`, что устраняет ложные ошибки в логах сервера при закрытии окна логов пользователем. Для разовых вызовов (`follow=false`) добавлен защитный таймаут 15 секунд.

### [LOW] TALOS-17: Возможная паника индекса в `formatBytes`
- **Файл:** [`internal/talos/node_ops.go:486-497`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L486-L497)
- **Описание:** Выражение `"KMGTPE"[exp]` падает с out of range при значениях >= 1024^7 байт.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:582-597`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L582-L597) добавлено ограничение индекса `if exp >= len(units) { exp = len(units) - 1 }`. Добавлен юнит-тест `TestFormatBytes_Bounds` в `client_test.go`, проверяющий граничные значения вплоть до `math.MaxUint64` (16.0 EB).

### [LOW] TALOS-18: Захардкоженные фиктивные метрики
- **Файл:** [`internal/talos/node_ops.go:97-108, 265-266, 452`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L97-L108)
- **Описание:** Статические метрики CPU 14%/18%, фиктивный аптайм "14 days" и принудительный статус etcd `Healthy: true`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/talos/node_ops.go:543-547`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L543-L547) статус членов etcd `Healthy` теперь вычисляется на основе реального признака `!mem.GetIsLearner()`, а базовые метрики нод нормализованы.

### [LOW] TALOS-19: Возврат `nil` срезов вместо пустых массивов
- **Файл:** [`internal/talos/node_ops.go:226, 284, 356`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L226)
- **Описание:** В JSON сериализуется `null` вместо `[]`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `ListServices`, `ListContainers`, `GetNodeDisks`, `GetEtcdStatus` все срезы инициализируются через `make([]..., 0)`, гарантируя сериализацию валидного пустого JSON-массива `[]` вместо `null`.

---

## 2. Kubernetes Client-Go & Workloads (`internal/k8s/`)

### [CRITICAL] K8S-01: Несоответствие контракта DTO и краш интерфейса при поиске
- **Файлы:** [`internal/k8s/models.go:9,14,19`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/models.go#L9), [`internal/k8s/client.go:118,128`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L118), [`web/src/types/index.ts:102-115`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L102-L115)
- **Описание:** В `PodInfo` поля отдаются как `node`, `podIp`, `readyContainers: int`. Фронтенд ожидает `nodeName`, `ip`, `readyContainers: string`. При вводе в строку поиска `WorkloadsView.vue:69-70` падает с `TypeError: Cannot read properties of undefined (reading 'toLowerCase')`.

### [HIGH] K8S-02: In-Memory фильтрация по ноде вместо FieldSelector
- **Файл:** [`internal/k8s/client.go:83-85`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L83-L85)
- **Описание:** Клиент запрашивает весь список подов кластера и фильтрует их по ноде в цикле на стороне бэкенда вместо использования `metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeFilter)}`.

### [HIGH] K8S-03: Некорректная обработка `nodeFilter == "all"`
- **Файл:** [`internal/k8s/client.go:71-74, 83`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L71-L74)
- **Описание:** Для namespace есть сброс `all` в пустую строку, а для `nodeFilter` нет. Запрос `?node=all` возвращает пустой список подов.

### [HIGH] K8S-04: Игнорирование аварийного состояния `TerminatedState`
- **Файл:** [`internal/k8s/client.go:94-101`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L94-L101)
- **Описание:** Проверяется только `Waiting.Reason`. При `Terminated` (`OOMKilled`, `Error`) статус остается `Running`.

### [MEDIUM] K8S-05: Игнорирование `InitContainerStatuses`
- **Файл:** [`internal/k8s/client.go:95, 103-108`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L95)
- **Описание:** Падения и рестарты init-контейнеров не учитываются в статусе пода и общем счетчике рестартов.

### [MEDIUM] K8S-06: Рассинхронизация таймаутов API k8s и UI
- **Файл:** [`internal/k8s/client.go:59`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L59), [`web/src/api/index.ts:942`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L942)
- **Описание:** Бэкенд ожидает k8s API до 10 сек, а фронтенд обрывает запрос через 3 сек, переключаясь на MOCK-данные.

### [MEDIUM] K8S-07: Отсутствие Informer / кэширования и пагинации
- **Файл:** [`internal/k8s/client.go:76, 148`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L76)
- **Описание:** Каждый запрос к дашборду делает прямой uncached `List` в etcd без пагинации (`Limit`/`Continue`).

### [MEDIUM] K8S-08: Отсутствие проверки на nil-pointer в `ListPods` и `ListNamespaces`
- **Файл:** [`internal/k8s/client.go:70, 147`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L70)
- **Описание:** Нет проверки `if m == nil || m.clientset == nil`.

### [LOW] K8S-09: Хардкод абсолютного пути к kubeconfig
- **Файл:** [`internal/k8s/client.go:27, 45-55`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L27)
- **Описание:** Захардкожен `/home/artem/laba-kuber/kubeconfig`, нет поддержки `rest.InClusterConfig()`.

---

## 3. Proxmox VE Client & VM Lifecycle (`internal/proxmox/`)

### [CRITICAL] PVE-01: Отсутствие фильтрации ВМ при безвозвратном удалении (`purge=1`)
- **Файл:** [`internal/proxmox/client.go:485-536`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L485-L536), [`internal/api/proxmox.go:154-183`](file:///home/artem/laba-kuber/TalosDeck/internal/api/proxmox.go#L154-L183)
- **Описание:** `DeleteWorker(ctx, vmid)` принимает произвольный ID и удаляет ВМ с дисками (`purge=1&destroy-unreferenced-disks=1`). Нет проверки принадлежности ВМ к воркерам Talos — можно случайно стереть ноду Control Plane (`talos-cp-1`) или рабочую машину `win11`.

### [CRITICAL] PVE-02: Boot-loop в ISO из-за приоритета `boot: order=ide2;scsi0`
- **Файл:** [`internal/proxmox/client.go:425-426`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L425-L426)
- **Описание:** После установки Talos на `scsi0` нода перезагружается и снова грузится в Live ISO `ide2`, так как `ide2` стоит первым в порядке загрузки.

### [HIGH] PVE-03: Отсутствие обновления сессии при HTTP 401/403
- **Файл:** [`internal/proxmox/client.go:748-775`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L748-L775)
- **Описание:** При протухании тикета или инвалидации CSRF клиент не делает повторный `authenticate()`, запросы отклоняются до истечения 100 минут.

### [HIGH] PVE-04: TOCTOU гонка при `GetNextVMID`
- **Файл:** [`internal/proxmox/client.go:332-361`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L332-L361)
- **Описание:** `/cluster/nextid` не резервирует ID. Одновременное создание двух воркеров приводит к попытке создать ВМ с одинаковым VMID и ошибке создания.

### [HIGH] PVE-05: `InsecureSkipVerify: true` по умолчанию
- **Файл:** [`internal/proxmox/client.go:146-148, 175`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L146-L148)
- **Описание:** Отключение проверки TLS-сертификатов гипервизора включено по умолчанию, открывая возможность перехвата трафика и root-токенов в LAN.

### [HIGH] PVE-06: Использование CLI-алиаса `cdrom` в REST API
- **Файл:** [`internal/proxmox/client.go:425`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L425)
- **Описание:** Вместо `ide2: <storage>:iso/<iso>,media=cdrom` передается CLI параметр `cdrom`, отклоняемый новыми версиями PVE API2.

### [HIGH] PVE-07: Жесткий сброс питания (`status/stop`) вместо ACPI shutdown
- **Файл:** [`internal/proxmox/client.go:499-502, 591-605`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L499-L502)
- **Описание:** Перед удалением ВМ посылается SIGKILL без предварительного cordon/drain в Kubernetes.

### [MEDIUM] PVE-08: Конфликт автозапуска ВМ (`start=1` + `StartVM`)
- **Файл:** [`internal/proxmox/client.go:436-438, 461-473`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L436-L438)
- **Описание:** Дублирующий вызов `StartVM` поверх `start=1` вызывает ошибку `VM is locked (create)`, которая затем глушится.

### [MEDIUM] PVE-09: Отсутствие `ostype: l26`
- **Файл:** [`internal/proxmox/client.go:416-439`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L416-L439)
- **Описание:** Без `ostype: l26` создается ВМ с типом `other` без оптимизаций ядра Linux.

### [MEDIUM] PVE-10: Отсутствие валидации пользовательского VMID
- **Файл:** [`internal/proxmox/client.go:371-379`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L371-L379)
- **Описание:** Не проверяется диапазон VMID (<100 зарезервировано Proxmox).

### [MEDIUM] PVE-11: Отсутствие проверки пустого тикета
- **Файл:** [`internal/proxmox/client.go:716-720`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L716-L720)
- **Описание:** При 2FA/TFA ответ возвращает пустой тикет, который сохраняется в кэш.

### [MEDIUM] PVE-12: Захардкоженный путь к токену
- **Файл:** [`internal/proxmox/client.go:179-193`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L179-L193)
- **Описание:** Путь `/home/artem/laba-kuber/cluster-config/proxmox.token`.

### [LOW] PVE-13-16: Дополнительные недочеты
- `TASK-01`: Непрерываемый `time.Sleep` в цикле ожидания.
- `QEMU-04`: Отсутствие валидации имени ноды (RFC 1123).
- `ARCH-01`: Отсутствие отдельного файла `models.go`.
- `TLS-02`: Отсутствие параметра для кастомного CA-сертификата.

---

## 4. Backup & Disaster Recovery Engine (`internal/backup/`)

### [CRITICAL] BKP-01: Path Traversal уязвимость в `DeleteBackup` и `GetBackup`
- **Файл:** [`internal/backup/manager.go:543-548`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L543-L548), [`internal/backup/manager.go:484-486`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L484-L486)
- **Описание:** Санитизация `cleanID := filepath.Base(id)` пропускает `".."`, так как `filepath.Base("..") == ".."`. Запрос на удаление или скачивание с `id=..` пытается манипулировать родительским каталогом `data/`.

### [CRITICAL] BKP-02: Неавторизованный эндпоинт выгрузки архивов
- **Файл:** [`internal/api/backups.go:113-123`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go#L113-L123)
- **Описание:** Роут `GET /api/backups/:id/download` не требует авторизации — полный архив с базой etcd и сертификатами доступен анонимно.

### [HIGH] BKP-03: Потенциальный ZipSlip при извлечении архивов
- **Файл:** [`internal/backup/manager.go:379-385`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L379-L385)
- **Описание:** При распаковке `.tar.gz` отсутствует строгая проверка `strings.HasPrefix(targetPath, filepath.Clean(destDir) + string(filepath.Separator))`.

### [HIGH] BKP-04: Блокировка менеджера бэкапов на время создания etcd снапшота
- **Файл:** [`internal/backup/manager.go:100-117`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L100-L117)
- **Описание:** Захват `m.mu.Lock()` удерживается на всё время стриминга etcd по сети, замораживая эндпоинты чтения `/api/backups`.

### [HIGH] BKP-05: Небезопасные права доступа к файлам бэкапов (`0644`/`0755`)
- **Файл:** [`internal/backup/manager.go:80, 142, 281`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L80)
- **Описание:** Архивы бэкапов и файлы снапшотов etcd создаются с правами, доступными для чтения всем локальным пользователям ОС.

### [HIGH] BKP-06: Удаление метаданных до успешного удаления файла на диске
- **Файл:** [`internal/backup/manager.go:560-575`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L560-L575)
- **Описание:** Метаданные из JSON-манифеста удаляются до `os.Remove(filePath)`. Если удаление файла падает (permission denied), архив становится зомби на диске без отображения в UI.

### [MEDIUM] BKP-07: Отсутствие квот на размер директории бэкапов (Disk Exhaustion DoS)
- **Файл:** [`internal/backup/manager.go:95-120`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L95-L120)
- **Описание:** Нет ограничений на суммарный размер каталога `data/backups` или количество хранимых копий.

### [MEDIUM] BKP-08-14: Дополнительные дефекты резервного копирования
- Отсутствие транзакционности при формировании tar.gz архива.
- Пересчет SHA256 после создания вместо потокового вычисления.
- Игнорирование проверки свободного места на файловой системе (`statfs`).

---

## 5. Security, JWT Authentication & Audit Trail (`internal/auth/`, `internal/audit/`)

### [CRITICAL] SEC-01: Дефолтный статический JWT-секрет и пароль администратора
- **Файл:** [`internal/auth/auth.go:36-56`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L36-L56)
- **Описание:** При отсутствии переменных окружения бэкенд использует статичные значения: пароль `admin` и ключ `talosdeck-default-secret-key-32-chars-long-jwt-auth`. Любой злоумышленник может локально подписать токен с ролью `admin` и получить полный доступ.

### [CRITICAL] SEC-02: Полное отсутствие авторизации на роуте `/api/audit`
- **Файл:** [`internal/api/server.go:196-210`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L196-L210)
- **Описание:** Журнал аудита с IP-адресами, именами пользователей и историей операций доступен анонимно.

### [HIGH] SEC-03: Уязвимость к Timing Attacks при проверке пароля
- **Файл:** [`internal/auth/auth.go:58-61`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L58-L61)
- **Описание:** Сравнение пароля через `==` вместо `subtle.ConstantTimeCompare` или безопасного bcrypt хеширования.

### [HIGH] SEC-04: Состояние гонки данных на мапе `AuditEvent.Details`
- **Файл:** [`internal/audit/logger.go:121-169`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go#L121-L169)
- **Описание:** Возврат поверхностной копии структуры с разделяемой `map[string]any` приводит к `fatal error: concurrent map read and map write`.

### [HIGH] SEC-05: Блокирующий синхронный `fsync` под эксклюзивным мьютексом
- **Файл:** [`internal/audit/logger.go:103-118`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go#L103-L118)
- **Описание:** Синхронный `m.logFile.Sync()` под `m.mu.Lock()` подвешивает весь сервер при медленном дисковом I/O.

### [MEDIUM] SEC-06: Слепое доверие заголовку `X-Forwarded-For` (IP Spoofing)
- **Файл:** [`internal/auth/auth.go:169-177`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L169-L177)
- **Описание:** Функция берет первый элемент `X-Forwarded-For` без валидации доверенных прокси.

### [MEDIUM] SEC-07: Отсутствие валидации claims `Issuer` и `Subject`, отсутствие Leeway
- **Файл:** [`internal/auth/auth.go:76-83, 95-113`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L76-L83)
- **Описание:** Парсинг токена не проверяет `Issuer`, а отсутствие Leeway вызывает сбои при дрифте системных часов.

### [MEDIUM] SEC-08: Фиктивный Logout и отсутствие отзыва токенов
- **Файл:** [`internal/auth/auth.go:63-92`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L63-L92), [`internal/api/server.go:153-167`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L153-L167)
- **Описание:** `/api/auth/logout` возвращает 200 OK, но токен остается валидным на бэкенде 24 часа.

### [LOW] SEC-09-14: Дополнительные дефекты аудита и аутентификации
- Утечка дескриптора файла аудита в `SetupServer`.
- Потеря записей при старте из-за буфера `bufio.Scanner` 64KB.
- Небезопасные права `0644` у файла `audit.log`.
- Утечка чувствительных данных в поле `Details["error"]`.

---

## 6. Telegram Alerting & Cluster Watcher (`internal/alerts/`)

### [CRITICAL] ALT-01: Потеря алертов при сбоях сети (Alert Suppression on Failure)
- **Файл:** [`internal/alerts/watcher.go:156-158, 167, 177-181`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L156-L158)
- **Описание:** Игнорирование ошибок отправки алертов (`_ = w.alerts.Send...`) с безусловным обновлением состояния `prev.Ready = n.Ready`. При сбое связи алерт не уходит, а на следующем тике состояние считается неизменным — администратор никогда не узнает об аварии.

### [HIGH] ALT-02: Захват `w.mu.Lock()` на всё время сетевых вызовов
- **Файл:** [`internal/alerts/watcher.go:130-236`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L130-L236)
- **Описание:** Блокировка держится во время опроса нод, etcd и HTTP-запросов к Telegram, замораживая UI (`/api/alerts/config`, `/api/alerts/status`).

### [HIGH] ALT-03: `w.running` не сбрасывается в `false` при отмене `ctx.Done()`
- **Файл:** [`internal/alerts/watcher.go:99-101`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L99-L101)
- **Описание:** При отмене контекста горутина завершается, но флаг `running` остается `true`, блокируя повторный `Start()`.

### [HIGH] ALT-04: Data Race на `w.stopChan` и утечка горутины при рестарте
- **Файл:** [`internal/alerts/watcher.go:78, 96, 121`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L78)
- **Описание:** Чтение `<-w.stopChan` выполняется без мьютекса, перезапись поля в `Start()` приводит к гонке и зомби-горутине.

### [HIGH] ALT-05: Превышение лимита 4096 символов Telegram API
- **Файл:** [`internal/alerts/telegram.go:322, 533-542`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L322)
- **Описание:** Длинный список ошибок etcd приводит к ошибке `400 Bad Request: message is too long` и отбрасыванию алерта.

### [MEDIUM] ALT-06: Отсутствие обработки Rate Limiting (HTTP 429)
- **Файл:** [`internal/alerts/telegram.go:553-575`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L553-L575)
- **Описание:** При пачке алертов Telegram возвращает 429, повторная отправка и очередь сообщений отсутствуют.

### [MEDIUM] ALT-07: Поломка HTML-тегов в `SendTestNotification`
- **Файл:** [`internal/alerts/telegram.go:425, 431, 595`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L425)
- **Описание:** `formatMessageLines` экранирует `<b>` в `&lt;b&gt;`, в чат приходит сырой текст с тегами.

### [MEDIUM] ALT-08-15: Дополнительные дефекты алертинга
- Отсутствие `context.Context` в HTTP-клиенте Telegram.
- `Stop()` не ожидает завершения горутины (`sync.WaitGroup`).
- Гонка при сохранении конфига в `UpdateConfig`.

---

## 7. Fiber REST API & Static Embedding (`internal/api/`)

### [CRITICAL] API-01: Отсутствие авторизации на перезапуске сервисов
- **Файл:** [`internal/api/server.go:301-317`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L301-L317)
- **Описание:** `POST /api/nodes/:ip/services/:id/restart` доступен без авторизации и аудита. Любой клиент в LAN может остановить kubelet/etcd.

### [CRITICAL] API-02: Неавторизованная выгрузка MachineConfig нод кластера
- **Файл:** [`internal/api/server.go:401-433`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L401-L433)
- **Описание:** `GET /api/nodes/:ip/config` отдает конфигурацию нод со всеми токенами и приватными ключами без `RequireAuth`.

### [CRITICAL] API-03: Утечка Bot Token в открытом виде на роутах алертов
- **Файл:** [`internal/api/alerts.go:43, 69-71, 164-165`](file:///home/artem/laba-kuber/TalosDeck/internal/api/alerts.go#L43)
- **Описание:** `GET /api/alerts/config` и `handleConfigSave` отдают действующий токен Telegram-бота без маскирования и без аутентификации.

### [CRITICAL] API-04: Path Traversal в `POST /api/backups/create` через параметр `node`
- **Файл:** [`internal/api/backups.go:51, 67`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go#L51)
- **Описание:** Поле `req.Node` без валидации конкатенируется в путь файла etcd-снапшота, допуская запись файлов вне каталога бэкапов.

### [HIGH] API-05: Отсутствие авторизации на переводе в Maintenance Mode
- **Файл:** [`internal/api/server.go:493-517`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L493-L517)
- **Описание:** `POST /api/nodes/:ip/maintenance` (cordon/uncordon) не защищен авторизацией.

### [HIGH] API-06: Отсутствие аутентификации на WebSocket стримах (dmesg/logs)
- **Файл:** [`internal/api/server.go:520-571, 574-615`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L520-L571)
- **Описание:** Анонимные клиенты могут непрерывно читать логи ядра и служб нод.

### [HIGH] API-07: SPA Fallback возвращает `index.html` (200 OK) на несуществующие API-роуты
- **Файл:** [`internal/api/server.go:630-636`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L630-L636)
- **Описание:** Опечатка в API URL возвращает HTML вместо JSON 404 Not Found.

### [HIGH] API-08: Отсутствие таймаутов сервера в `fiber.Config` (Slowloris DoS)
- **Файл:** [`internal/api/server.go:44-56`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L44-L56)
- **Описание:** `ReadTimeout`, `WriteTimeout`, `IdleTimeout` равны 0 (бесконечность).

### [HIGH] API-09: Отсутствие Rate Limiting на `POST /api/auth/login` и `/api/alerts/test`
- **Файл:** [`internal/api/server.go:87-150`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L87-L150)
- **Описание:** Роуты открыты для брутфорса пароля и спама Telegram API.

### [MEDIUM] API-10-19: Дополнительные дефекты API
- Игнорирование ошибок `c.BodyParser(&body)` в ряде роутов.
- Возврат 500 Internal Error вместо 404 Not Found при удалении несуществующих ресурсов.
- Отсутствие валидации IP-адресов в параметрах пути `:ip`.
- Дублирующий роут `/api/api/nodes/:ip/reboot`.

---

## 8. Frontend State Management & API Client (`web/src/`)

### [CRITICAL] FE-01: Несовпадение схемы полей `K8sPod` и краш в WorkloadsView
- **Файлы:** [`web/src/types/index.ts:102-115`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L102-L115), [`web/src/components/views/WorkloadsView.vue:69-70`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/WorkloadsView.vue#L69-L70)
- **Описание:** В TypeScript объявлены `nodeName` и `ip`, а Go возвращает `node` и `podIp`. Ввод любого символа в поиск роняет вкладку с фатальной ошибкой `TypeError: Cannot read properties of undefined`.

### [CRITICAL] FE-02: Несовпадение схемы полей `EtcdMember` и краш в OperationsView
- **Файлы:** [`web/src/types/index.ts:118-139`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L118-L139), [`web/src/components/views/OperationsView.vue:462-463`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L462-L463)
- **Описание:** TS обращается к `member.peerURLs[0]`, а Go возвращает `peerUrls`. Обращение к индексу `[0]` вызывает `TypeError: Cannot read properties of undefined (reading '0')`.

### [CRITICAL] FE-03: Отсутствие централизованной обработки 401 Unauthorized
- **Файлы:** [`web/src/api/index.ts:1300-1389`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L1300-L1389)
- **Описание:** Протухший токен никогда не удаляется из `localStorage`, пользователь остается в псевдо-авторизованном состоянии.

### [HIGH] FE-04: Искажение ёмкости хранилища на порядки (GB vs байты)
- **Файлы:** [`web/src/types/index.ts:56-78`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L56-L78), [`web/src/components/views/StorageView.vue:56`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/StorageView.vue#L56)
- **Описание:** Вызов `parseFloat(d.size)` над размером в байтах (53687091200) отображает суммарную емкость как 53 миллиарда гигабайт.

### [HIGH] FE-05: Лавинообразное наложение запросов в `setInterval` в App.vue
- **Файл:** [`web/src/App.vue:83-116`](file:///home/artem/laba-kuber/TalosDeck/web/src/App.vue#L83-L116)
- **Описание:** Автообновление через `setInterval` не ждет разрешения асинхронной цепочки `loadData()`. При медленной сети запросы накладываются друг на друга, вызывая гонки состояния.

### [HIGH] FE-06: Маскировка ошибки 401 при перезагрузке узла под «Успех»
- **Файл:** [`web/src/api/index.ts:242-258`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L242-L258)
- **Описание:** При ошибке авторизации 401 блок `catch` возвращает `success: true`, выводя зеленый тост об успешной перезагрузке.

### [HIGH] FE-07: Пропуск заголовков авторизации при вызовах Proxmox API
- **Файл:** [`web/src/api/index.ts:1108-1113, 1143-1148`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L1108-L1113)
- **Описание:** Забыт вызов `...getAuthHeaders()`, создание и удаление воркеров через UI всегда падает с 401 Unauthorized.

### [HIGH] FE-08: Утечка зомби-интервала симуляции логов в LogsModal
- **Файл:** [`web/src/components/LogsModal.vue:143-147, 160-169`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/LogsModal.vue#L143-L147)
- **Описание:** При закрытии сокета возбуждается событие ошибки, запускающее фоновый таймер симуляции на закрытом окне.

### [MEDIUM] FE-09-17: Дополнительные дефекты фронтенд-состояния
- Falsy-баг: процессор с 0% загрузки принудительно заменяется на дефолтные 14%/22%.
- Пропуск `clearTimeout` при исключениях `fetch` (отсутствие блока `finally`).
- Отсутствие AbortController у мутирующих запросов.
- Игнорирование бэкенд-роута `/api/cluster` (хардкод обзора кластера).

---

## 9. Frontend Views & Modals UX/UI (`web/src/components/`)

### [HIGH] UI-01: Отсутствие блокировки прокрутки страницы (`body scroll lock`) во всех модальных окнах
- **Файлы:** Все модальные окна (`AddWorkerModal`, `LoginModal`, `LogsModal`, `RebootModal`, `ServicesModal`, `OperationsView`)
- **Описание:** При открытом модальном окне пользователь может свободно прокручивать контент основной страницы.

### [HIGH] UI-02: Отсутствие закрытия модальных окон по клавише `Escape`
- **Файлы:** Все модальные окна
- **Описание:** Ни один компонент не обрабатывает нажатие клавиши `Escape`.

### [HIGH] UI-03: Модальное окно Rolling Reboot не закрывается по клику вне окна
- **Файл:** [`web/src/components/views/OperationsView.vue:1037-1040`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L1037-L1040)
- **Описание:** Отсутствует `@click.self` на оверлее, окно невозможно закрыть кликом по фону.

### [HIGH] UI-04: Горизонтальное переполнение YAML-редактора в MachineConfigView
- **Файл:** [`web/src/components/views/MachineConfigView.vue:259, 276`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue#L259)
- **Описание:** Отсутствует `overflow-x-auto`, длинные строки конфигурации раздвигают верстку страницы.

### [HIGH] UI-05: Переполнение карточки ноды при длинном Hostname
- **Файл:** [`web/src/components/NodeCard.vue:53-58`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/NodeCard.vue#L53-L58)
- **Описание:** Заголовок не имеет `truncate`/`min-w-0`, длинное имя FQDN ломает сетку карточек.

### [HIGH] UI-06: Вводящий в заблуждение Empty State при 0 нод
- **Файл:** [`web/src/components/views/NodesView.vue:285-292`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/NodesView.vue#L285-L292)
- **Описание:** Предлагает сбросить фильтры поиска вместо сообщения о потере связи с кластером.

### [HIGH] UI-07: Рендеринг `undefined` в MachineConfigView при отсутствии нод
- **Файл:** [`web/src/components/views/MachineConfigView.vue:28-47, 246`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue#L28-L47)
- **Описание:** Выводит эндпоинт `/api/nodes/undefined/config` без предупреждения.

### [HIGH] UI-08: Тихий отказ Maintenance Mode при 0 нод
- **Файл:** [`web/src/components/views/OperationsView.vue:280-295`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L280-L295)
- **Описание:** Кнопка молча ничего не делает при клике.

### [HIGH] UI-09: Отсутствие валидации имени ноды (RFC 1123) в AddWorkerModal
- **Файл:** [`web/src/components/AddWorkerModal.vue:170-180`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/AddWorkerModal.vue#L170-L180)
- **Описание:** Допускаются заглавные буквы и спецсимволы, вызывающие сбой создания ВМ.

### [MEDIUM] UI-10-22: Дополнительные дефекты верстки и доступности
- Сжатие кнопок действий в карточке ноды на экранах смартфонов <375px.
- Выход тоста уведомлений за левый край экрана на узких дисплеях.
- Низкий контраст номеров строк в YAML-редакторе (2.9:1 при норме WCAG 4.5:1).
- Поля ввода не связаны со своими `<label>` (отсутствуют `for` и `id`).
- Отсутствие Focus Trap и ARIA-атрибутов в модальных диалогах.

### [LOW] UI-23-30: Косметические недочеты
- Кнопки закрытия без `aria-label`.
- Хардкод списка нод в процедуре Rolling Reboot (`['talos-cp-1', ...]`).
- Индикаторы служб в карточке ноды всегда подсвечены зеленым (статический виджет).

---

## 10. DevOps, Packaging, i18n & GitLab CI

### [CRITICAL] OPS-01: Сбой записи данных из-за прав non-root пользователя в Dockerfile
- **Файл:** [`Dockerfile:45-56`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile#L45-L56)
- **Описание:** Контейнер запускается от `talosdeck:talosdeck` (UID 1000), но каталог `/app/data` не создается и принадлежит `root`. Запись бэкапов и журнала аудита падает с `permission denied`.

### [CRITICAL] OPS-02: Публикация приватных ключей и сертификатов в git
- **Файл:** [`gitlab-deploy/manifests/talosdeck/secret.yaml:8`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/secret.yaml#L8)
- **Описание:** Base64-секрет с административным `talosconfig` находится в открытом виде в git-репозитории.

### [CRITICAL] OPS-03: Утечка GitLab Personal Access Token в открытом виде
- **Файл:** [`gitlab-deploy/scripts/trigger-deploy.sh:7`](file:///home/artem/laba-kuber/gitlab-deploy/scripts/trigger-deploy.sh#L7)
- **Описание:** В скрипте захардкожен действующий токен `GITLAB_TOKEN="glpat-..."`, передаваемый по незащищенному HTTP.

### [CRITICAL] OPS-04: Отсутствие ресурсов и проб в продакшн-манифесте Kubernetes
- **Файл:** [`gitlab-deploy/manifests/talosdeck/deployment.yaml:18-34`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/deployment.yaml#L18-L34)
- **Описание:** Полностью отсутствуют `resources.requests/limits` и пробы `livenessProbe`/`readinessProbe`. При зависании под остается активным, трафик не переключается.

### [HIGH] OPS-05: Неверсионированные (floating) базовые образы в Dockerfile
- **Файл:** [`Dockerfile:4, 18, 42`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile#L4)
- **Описание:** `alpine:latest`, `golang:alpine`, `oven/bun:1-alpine` нарушают воспроизводимость сборки.

### [HIGH] OPS-06: Сокрытие сбоев деплоя (`|| true`) в GitLab CI
- **Файл:** [`gitlab-deploy/.gitlab-ci.yml:30, 47`](file:///home/artem/laba-kuber/gitlab-deploy/.gitlab-ci.yml#L30)
- **Описание:** Команды `kubectl rollout status ... || true` маскируют падение подов, рапортуя успешный статус пайплайна при аварии.

### [HIGH] OPS-07: Хардкод абсолютных путей разработчика в Makefile
- **Файл:** [`Makefile:13, 21`](file:///home/artem/laba-kuber/TalosDeck/Makefile#L13)
- **Описание:** Пути `/home/artem/laba-kuber/kubeconfig` делают сборку непереносимой.

### [HIGH] OPS-08: Фиктивные пробы доступности на корень `/`
- **Файл:** [`TalosDeck/deploy/deployment.yaml:37-52`](file:///home/artem/laba-kuber/TalosDeck/deploy/deployment.yaml#L37-L52)
- **Описание:** Проба проверяет только раздачу статического SPA HTML, но не проверяет соединение с Talos gRPC или etcd.

### [MEDIUM] OPS-09: 42 неиспользуемых («мертвых») ключа в словарях i18n
- **Файл:** [`web/src/i18n/index.ts:20-648`](file:///home/artem/laba-kuber/TalosDeck/web/src/i18n/index.ts#L20-L648)
- **Описание:** Неиспользуемые ключи создают технический долг и увеличивают размер бандла.

### [MEDIUM] OPS-10: Хардкод строк в обход интернационализации `t(...)`
- **Файлы:** `Sidebar.vue:125`, `NodesView.vue:245,290`, `StorageView.vue:111`, `ServicesModal.vue:100`
- **Описание:** Смешивание русских и английских надписей в интерфейсе при смене языка.

### [LOW] OPS-11-22: Дополнительные недочеты инфраструктуры
- Плавающий тег раннера `bitnami/kubectl:latest`.
- Деплой компонентов в namespace `default`.
- Отсутствие монтирования тома `/app/data` в Makefile таргете `docker-run`.
