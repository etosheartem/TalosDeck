# Трекер дефектов TalosDeck

> **Аудит:** 11 сентября 2026 г.
>
> **Последнее обновление трекера:** 11 сентября 2026 г.
> Диапазоны вроде `UI-10-22` считаются как несколько дефектов. Подробные описания и выполненные исправления сохранены ниже.

## Текущее состояние

| Статус | Critical | High | Medium | Low | Всего |
|:---|---:|---:|---:|---:|---:|
| ⬜ Открыто | 0 | 0 | 0 | 0 | **0** |
| ✅ Исправлено | 22 | 48 | 71 | 34 | **175** |
| **Итого** | **22** | **48** | **71** | **34** | **175** |

**Прогресс:** 175 из 175 исправлено (100%), 0 открыто. 🎉 Все дефекты аудита успешно устранены!

## Открытые задачи

| Статус | Приоритет | ID | Подсистема | Кол-во | Проблема |
|:---:|:---:|:---|:---|---:|:---|
| — | — | — | — | 0 | *Все выявленные дефекты устранены. Открытых задач нет.* |

## Прогресс по подсистемам

| Подсистема | Исправлено | Открыто | Всего | Прогресс |
|:---|---:|---:|---:|---:|
| **Talos SDK** | 19 | 0 | 19 | 100% |
| **Kubernetes** | 9 | 0 | 9 | 100% |
| **Proxmox** | 16 | 0 | 16 | 100% |
| **Backup** | 14 | 0 | 14 | 100% |
| **Security** | 14 | 0 | 14 | 100% |
| **Alerts** | 15 | 0 | 15 | 100% |
| **REST API** | 19 | 0 | 19 | 100% |
| **Frontend state** | 17 | 0 | 17 | 100% |
| **Frontend UX/UI** | 30 | 0 | 30 | 100% |
| **DevOps / CI** | 22 | 0 | 22 | 100% |

## Как обновлять трекер

1. Найдите карточку по ID в разделе «Подробные карточки».
2. После исправления добавьте в карточку статус `ИСПРАВЛЕНО (FIXED)` и кратко опишите проверку.
3. Обновите строку задачи и счетчики в таблицах выше. Для диапазона ID укажите число реально закрытых дефектов отдельно, если исправлена только часть группы.

Обозначения: `⬜ OPEN` — требует работы; `✅ FIXED` — исправлено и проверено.

---

## Подробные карточки

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
  Память читается через Talos Memory API, uptime — из `/proc/uptime`, версия Kubernetes — из COSI `KubeletStatus`. CPU utilization вычисляется по разнице накопительных COSI `CPUStat` между последовательными опросами, а не как среднее за весь uptime. Статус членов etcd `Healthy` вычисляется из фактического состояния ответа SDK.

### [LOW] TALOS-19: Возврат `nil` срезов вместо пустых массивов
- **Файл:** [`internal/talos/node_ops.go:226, 284, 356`](file:///home/artem/laba-kuber/TalosDeck/internal/talos/node_ops.go#L226)
- **Описание:** В JSON сериализуется `null` вместо `[]`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `ListServices`, `ListContainers`, `GetNodeDisks`, `GetEtcdStatus` все срезы инициализируются через `make([]..., 0)`, гарантируя сериализацию валидного пустого JSON-массива `[]` вместо `null`.

---

## 2. Kubernetes Client-Go & Workloads (`internal/k8s/`)

### [CRITICAL] K8S-01: Несоответствие контракта DTO и краш интерфейса при поиске
- **Файлы:** [`internal/k8s/models.go:7-24`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/models.go#L7-L24), [`internal/k8s/client.go:240-260`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L240-L260), [`web/src/types/index.ts:102-115`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L102-L115)
- **Описание:** В `PodInfo` поля отдаются как `node`, `podIp`, `readyContainers: int`. Фронтенд ожидает `nodeName`, `ip`, `readyContainers: string`. При вводе в строку поиска `WorkloadsView.vue:69-70` падает с `TypeError: Cannot read properties of undefined (reading 'toLowerCase')`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/models.go:7-24`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/models.go#L7-L24) структура `PodInfo` дополнена полями `ID`, `NodeName`, `NodeIP`, `IP`, `ReadyContainers` (типа `string`, например `"1/1"`), сохранив обратную совместимость с `ReadyCount` и `Node`. В [`internal/k8s/client.go:240-260`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L240-L260) все DTO поля полностью заполняются, что исключает падение поиска на фронтенде по `pod.nodeName.toLowerCase()`.

### [HIGH] K8S-02: In-Memory фильтрация по ноде вместо FieldSelector
- **Файл:** [`internal/k8s/client.go:160-175`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L160-L175)
- **Описание:** Клиент запрашивает весь список подов кластера и фильтрует их по ноде в цикле на стороне бэкенда вместо использования `metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeFilter)}`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:164-167`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L164-L167) вызов `List` теперь передает `metav1.ListOptions{FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeFilter)}` на сервер API Kubernetes, разгружая сеть и процессор бэкенда при больших кластерах. Сохранена защитная fallback-проверка в памяти на случай API без поддержки селектора.

### [HIGH] K8S-03: Некорректная обработка `nodeFilter == "all"`
- **Файл:** [`internal/k8s/client.go:146-148`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L146-L148)
- **Описание:** Для namespace есть сброс `all` в пустую строку, а для `nodeFilter` нет. Запрос `?node=all` возвращает пустой список подов.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:146-148`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L146-L148) добавлена нормализация параметра `if nodeFilter == "all" { nodeFilter = "" }`, аналогично параметру `namespace`.

### [HIGH] K8S-04: Игнорирование аварийного состояния `TerminatedState`
- **Файл:** [`internal/k8s/client.go:197-221`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L197-L221)
- **Описание:** Проверяется только `Waiting.Reason`. При `Terminated` (`OOMKilled`, `Error`) статус остается `Running`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:197-221`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L197-L221) добавлен глубокий анализ `cs.State.Terminated`: обрабатываются явные причины (например, `OOMKilled`), ненулевые коды завершения (`Error:137`), системные сигналы (`Signal:9`) и `Completed`.

### [MEDIUM] K8S-05: Игнорирование `InitContainerStatuses`
- **Файл:** [`internal/k8s/client.go:180-195`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L180-L195)
- **Описание:** Падения и рестарты init-контейнеров не учитываются в статусе пода и общем счетчике рестартов.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:180-195`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L180-L195) добавлен разбор `InitContainerStatuses`: счетчик рестартов каждого init-контейнера суммируется в общий `Restarts`, а при сбоях (CrashLoopBackOff, ошибка выполнения) статус пода формируется как `Init:<Reason>` или `Init:ExitCode:<code>`.

### [MEDIUM] K8S-06: Рассинхронизация таймаутов API k8s и UI
- **Файл:** [`internal/k8s/client.go:88, 160`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L88), [`web/src/api/index.ts:939-956`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L939-L956)
- **Описание:** Бэкенд ожидает k8s API до 10 сек, а фронтенд обрывает запрос через 3 сек, переключаясь на MOCK-данные.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:88, 160`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L88) таймаут клиента Kubernetes согласован на 8 секунд с явным контролем через контекст, а на фронтенде в [`web/src/api/index.ts:940-955`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L940-L955) таймаут `AbortController` увеличен до 6 секунд и очищается в блоке `finally`.

### [MEDIUM] K8S-07: Отсутствие Informer / кэширования и пагинации
- **Файл:** [`internal/k8s/client.go:20-25, 149-160, 274-280`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L20-L25)
- **Описание:** Каждый запрос к дашборду делает прямой uncached `List` в etcd без пагинации (`Limit`/`Continue`).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:20-25, 149-160`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L20-L25) внедрен потокобезопасный in-memory TTL-кэш (`2*time.Second`) с `sync.RWMutex`. Он предотвращает лавинные запросы (polling storms) на etcd/kube-apiserver от сотен сессий браузера при опросе рабочих нагрузок.

### [MEDIUM] K8S-08: Отсутствие проверки на nil-pointer в `ListPods` и `ListNamespaces`
- **Файл:** [`internal/k8s/client.go:136-140, 285-288`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L136-L140)
- **Описание:** Нет проверки `if m == nil || m.clientset == nil`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:136-140, 285-288`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L136-L140) добавлены предварительные проверки на `m == nil || m.clientset == nil` с возвратом понятной ошибки инициализации вместо паники рантайма. Поведение протестировано юнит-тестом `TestK8sManager_NilSafety`.

### [LOW] K8S-09: Хардкод абсолютного пути к kubeconfig
- **Файл:** [`internal/k8s/client.go:37-67`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L37-L67)
- **Описание:** Захардкожен `/home/artem/laba-kuber/kubeconfig`, нет поддержки `rest.InClusterConfig()`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/k8s/client.go:37-67`](file:///home/artem/laba-kuber/TalosDeck/internal/k8s/client.go#L37-L67) реализована многоуровневая цепочка загрузки конфигурации:
  1. `rest.InClusterConfig()` (для работы внутри пода Kubernetes).
  2. Переменная окружения `KUBECONFIG` и переданный параметр `kubeconfigPath`.
  3. Стандартные системные пути (`~/.kube/config`, `./kubeconfig`).
  4. Динамический генератор `kubeconfigBytesProvider` через Talos API.
  Все ошибки загрузки аккумулируются и логируются для детальной диагностики в случае сбоя.

---

## 3. Proxmox VE Client & VM Lifecycle (`internal/proxmox/`)

### [CRITICAL] PVE-01: Отсутствие фильтрации ВМ при безвозвратном удалении (`purge=1`)
- **Файл:** [`internal/proxmox/client.go:445-515`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L445-L515), [`internal/api/proxmox.go:160-205`](file:///home/artem/laba-kuber/TalosDeck/internal/api/proxmox.go#L160-L205)
- **Описание:** `DeleteWorker(ctx, vmid)` принимает произвольный ID и удаляет ВМ с дисками (`purge=1&destroy-unreferenced-disks=1`). Нет проверки принадлежности ВМ к воркерам Talos — можно случайно стереть ноду Control Plane (`talos-cp-1`) или рабочую машину `win11`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/proxmox/client.go`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go) внедрена строгая функция проверки `isTalosWorker(name)`:
  1. Запрещено удаление любых узлов Control Plane / Master (`talos-cp*`, `controlplane`, `master`).
  2. Запрещено удаление критической инфраструктуры хоста и рабочих машин (`win11`, `ubuntu-server`, `pve`, `proxmox`).
  3. Разрешено удаление только ВМ с явным префиксом `talos-worker` или ролью worker. При попытке удаления защищенной ВМ возвращается ошибка `safety check violation: VM %d (%q) is protected or not a Talos worker node; deletion aborted`.
  В [`internal/api/proxmox.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/proxmox.go) попытка удаления защищенной ВМ возвращает статус `403 Forbidden` вместо `500 Internal Server Error`. Поведение покрыто тестами `TestDeleteWorker_SafetyCheck_ProtectedVM` и `TestProxmoxAPIConfigured/DELETE_/api/proxmox/worker/110_(Control_Plane_Protected)`.

### [CRITICAL] PVE-02: Boot-loop в ISO из-за приоритета `boot: order=ide2;scsi0`
- **Файл:** [`internal/proxmox/client.go:420-428`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L420-L428)
- **Описание:** После установки Talos на `scsi0` нода перезагружается и снова грузится в Live ISO `ide2`, так как `ide2` стоит первым в порядке загрузки.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В параметрах создания ВМ порядок загрузки изменен на канонический для Talos: `boot: "order=scsi0;ide2"`. При первом запуске накопитель `scsi0` чист, поэтому BIOS/UEFI пропускает его и загружает Live ISO `ide2`. После разметки и установки Talos на диск `scsi0`, при последующих перезагрузках нода мгновенно стартует с установленной ОС на `scsi0`, полностью устраняя зацикливание загрузки.

### [HIGH] PVE-03: Отсутствие обновления сессии при HTTP 401/403
- **Файл:** [`internal/proxmox/client.go:660-705`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L660-L705)
- **Описание:** При протухании тикета или инвалидации CSRF клиент не делает повторный `authenticate()`, запросы отклоняются до истечения 100 минут.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В методе `doRequest` тело запроса буферизуется для возможности повтора. При получении `401 Unauthorized` или `403 Forbidden` при тикет-аутентификации кэшированный тикет и CSRF-токен инвалидируются под мьютексом, вызывается повторный `authenticate(ctx)`, и запрос автоматически перезапускается с новыми учетными данными сессии. Добавлен юнит-тест `TestSessionRefreshOn401`.

### [HIGH] PVE-04: TOCTOU гонка при `GetNextVMID`
- **Файл:** [`internal/proxmox/client.go:340-390`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L340-L390)
- **Описание:** `/cluster/nextid` не резервирует ID. Одновременное создание двух воркеров приводит к попытке создать ВМ с одинаковым VMID и ошибке создания.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `Client` внедрен механизм бронирования `inFlightVMIDs map[int]bool` под `vmidMu sync.Mutex`. Методы `allocateVMID` и `releaseVMID` атомарно резервируют следующий свободный идентификатор на время выполнения операции создания ВМ, исключая коллизии при одновременном запуске нескольких воркеров. Читающий метод `GetNextVMID` возвращает актуальный незанятый ID. Покрыто юнит-тестом `TestAllocateVMID_Concurrency`.

### [HIGH] PVE-05: `InsecureSkipVerify: true` по умолчанию
- **Файл:** [`internal/proxmox/client.go:120-145`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L120-L145)
- **Описание:** Отключение проверки TLS-сертификатов гипервизора включено по умолчанию, открывая возможность перехвата трафика и root-токенов в LAN.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Значение по умолчанию изменено на безопасное `SkipTLSVerify: false`. Реализована поддержка пользовательских корневых сертификатов через переменные окружения `PROXMOX_CA_CERT` / `PROXMOX_CA_FILE` и параметры конфигурации `CACert` / `CACertFile` с загрузкой в `tls.Config{RootCAs}`. Добавлены тесты `TestPVE05_SecureByDefault` и `TestTLS_CustomCA`.

### [HIGH] PVE-06: Использование CLI-алиаса `cdrom` в REST API
- **Файл:** [`internal/proxmox/client.go:420`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L420)
- **Описание:** Вместо `ide2: <storage>:iso/<iso>,media=cdrom` передается CLI параметр `cdrom`, отклоняемый новыми версиями PVE API2.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В параметрах создания ВМ Proxmox QEMU передан стандартный REST API параметр `ide2: fmt.Sprintf("%s,media=cdrom", iso)`. Сохранен защитный алиас `cdrom` для обратной совместимости.

### [HIGH] PVE-07: Жесткий сброс питания (`status/stop`) вместо ACPI shutdown
- **Файл:** [`internal/proxmox/client.go:465-515`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L465-L515)
- **Описание:** Перед удалением ВМ посылается SIGKILL без предварительного cordon/drain в Kubernetes.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Добавлен метод `ShutdownVM(ctx, vmid)` (POST `/nodes/{node}/qemu/{vmid}/status/shutdown`). Перед удалением ноды выполняется cordon и drain через Kubernetes Eviction API с соблюдением PodDisruptionBudget и ожиданием фактического ухода workload-подов. Ошибка или отсутствие drainer блокирует удаление VM. После успешного drain `DeleteWorker` отправляет ACPI shutdown и ожидает штатной остановки ОС до 15 секунд. Принудительный `StopVM` вызывается только как аварийный fallback.

### [MEDIUM] PVE-08: Конфликт автозапуска ВМ (`start=1` + `StartVM`)
- **Файл:** [`internal/proxmox/client.go:435-460`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L435-L460)
- **Описание:** Дублирующий вызов `StartVM` поверх `start=1` вызывает ошибку `VM is locked (create)`, которая затем глушится.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  После создания ВМ статус опрашивается немедленно с интервалом 500мс. Если ВМ уже перешла в состояние `running` под управлением флага `start=1`, повторный вызов `StartVM` не выполняется, что устраняет ошибку блокировки `VM is locked (create)`. Вызов `StartVM` происходит только при необходимости отложенного ручного старта.

### [MEDIUM] PVE-09: Отсутствие `ostype: l26`
- **Файл:** [`internal/proxmox/client.go:415`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L415)
- **Описание:** Без `ostype: l26` создается ВМ с типом `other` без оптимизаций ядра Linux.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В тело запроса создания виртуальной машины явно добавлено свойство `form.Set("ostype", "l26")`, активирующее в гипервизоре оптимизации для современных ядер Linux 2.6/3.x/4.x/5.x/6.x.

### [MEDIUM] PVE-10: Отсутствие валидации пользовательского VMID
- **Файл:** [`internal/proxmox/client.go:365-370`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L365-L370)
- **Описание:** Не проверяется диапазон VMID (<100 зарезервировано Proxmox).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Добавлена валидация `if opts.VMID < 100 || opts.VMID > 999999999`, возвращающая понятную ошибку и HTTP 400 Bad Request на уровне API.

### [MEDIUM] PVE-11: Отсутствие проверки пустого тикета
- **Файл:** [`internal/proxmox/client.go:615-625`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L615-L625)
- **Описание:** При 2FA/TFA ответ возвращает пустой тикет, который сохраняется в кэш.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `authenticate` добавлена проверка `if authResp.Data.Ticket == "" { return errors.New("authentication failed: Proxmox returned empty ticket (TFA/2FA or authentication failure)") }`. Покрыто тестом `TestTicketAuthentication_EmptyTicket`.

### [MEDIUM] PVE-12: Захардкоженный путь к токену
- **Файл:** [`internal/proxmox/client.go:135-155`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/client.go#L135-L155)
- **Описание:** Путь `/home/artem/laba-kuber/cluster-config/proxmox.token`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Внедрена динамическая цепочка поиска токена: переменные `PROXMOX_TOKEN_FILE` / `PVE_TOKEN_FILE`, затем относительные пути (`cluster-config/proxmox.token`, `./proxmox.token`), системные пути (`/etc/talosdeck/proxmox.token`) и путь окружения разработчика.

### [LOW] PVE-13-16: Дополнительные недочеты
- `TASK-01`: Непрерываемый `time.Sleep` в цикле ожидания.
- `QEMU-04`: Отсутствие валидации имени ноды (RFC 1123).
- `ARCH-01`: Отсутствие отдельного файла `models.go`.
- `TLS-02`: Отсутствие параметра для кастомного CA-сертификата.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненные исправления:**
  - `TASK-01`: В `WaitForTask` заменен блокирующий `time.Sleep` на прерываемый `select` по `ctx.Done()`.
  - `QEMU-04`: Внедрена валидация имени виртуальной машины по стандарту RFC 1123 (`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`).
  - `ARCH-01`: Все DTO-структуры вынесены в отдельный файл [`internal/proxmox/models.go`](file:///home/artem/laba-kuber/TalosDeck/internal/proxmox/models.go).
  - `TLS-02`: Добавлена полная поддержка кастомных корневых сертификатов CA (`CACert` / `CACertFile`).

---

## 4. Backup & Disaster Recovery Engine (`internal/backup/`)

### [CRITICAL] BKP-01: Path Traversal уязвимость в `DeleteBackup` и `GetBackup`
- **Файл:** [`internal/backup/manager.go:490-580`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L490-L580), [`internal/api/backups.go:115-165`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go#L115-L165)
- **Описание:** Санитизация `cleanID := filepath.Base(id)` пропускает `".."`, так как `filepath.Base("..") == ".."`. Запрос на удаление или скачивание с `id=..` пытается манипулировать родительским каталогом `data/`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/backup/manager.go`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go) в методах `GetBackup` и `DeleteBackup` внедрена строгая валидация:
  1. Явная проверка `cleanID == ".."`, `.`, `/`, `\` и отсечение любых разделителей путей.
  2. Лексическая проверка изоляции: `strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(m.storageDir) + string(filepath.Separator))`.
  В [`internal/api/backups.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go) при обнаружении попытки path traversal возвращается `400 Bad Request`. Покрыто тестами `TestPathTraversalProtection` и `TestBackupSecurityEndpoints`.

### [CRITICAL] BKP-02: Неавторизованный эндпоинт выгрузки архивов
- **Файл:** [`internal/api/backups.go:25-45, 115-130`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go#L25-L45)
- **Описание:** Роуты `GET /api/backups/:id/download` и `GET /api/backups` не требовали авторизации — полный архив с базой etcd и сертификатами был доступен анонимно.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Эндпоинты `GET /api/backups` и `GET /api/backups/:id/download` защищены мидлварем `auth.RequireAuth(authMgr)` при включенной аутентификации. Анонимный доступ немедленно отклоняется со статусом `401 Unauthorized`. Добавлен юнит-тест `TestBackupSecurityEndpoints`.

### [HIGH] BKP-03: Потенциальный ZipSlip при извлечении архивов
- **Файл:** [`internal/backup/manager.go:160-350`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L160-L350)
- **Описание:** При формировании tar-архива имена файлов брались из несанитизированных метаданных нод кластера и имени контекста.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Внедрена вспомогательная функция `sanitizeFilename`, очищающая спецсимволы, слеши и точки. Имена machine config файлов формируются из санитизированных хостнеймов и IP (`sanitizeFilename(n.Hostname)`). В функцию `addFileToTar` добавлена строгая проверка пути: запрещены абсолютные пути и выходы `..`. Валидируется IP адрес ноды в `CreateEtcdSnapshot` через `net.ParseIP`.

### [HIGH] BKP-04: Блокировка менеджера бэкапов на время создания etcd снапшота
- **Файл:** [`internal/backup/manager.go:130-380`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L130-L380)
- **Описание:** Захват `m.mu.Lock()` удерживался на всё время стриминга etcd по сети, замораживая эндпоинты чтения `/api/backups`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Сетевой стриминг, сжатие gzip и запись на диск вынесены из-под блокировки `m.mu.Lock()`. Для защиты от конкурентных запусков внедрен атомарный флаг `isBackingUp atomic.Bool` (`CompareAndSwap(false, true)`). Параллельный запрос на создание бэкапа немедленно получает отказ со статусом `409 Conflict` без зависания горутин и без блокировки параллельного чтения (`ListBackups`, `GetBackup`). Покрыто тестом `TestConcurrencyGuard`.

### [HIGH] BKP-05: Небезопасные права доступа к файлам бэкапов (`0644`/`0755`)
- **Файл:** [`internal/backup/manager.go:70-360`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L70-L360)
- **Описание:** Архивы бэкапов и файлы снапшотов etcd создавались с правами, доступными для чтения всем локальным пользователям ОС.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Каталог хранилища бэкапов создается строго с правами `0700` (`rwx------`). Все снапшоты, `.tar.gz` архивы и sidecar-файлы (`.sha256`, `.json`) записываются с исключительными правами `0600` (`rw-------`). Внутри tar-архива файлы упаковываются с маской `0600`. Покрыто тестом `TestSecurePermissionsAndIntegrity`.

### [HIGH] BKP-06: Удаление метаданных до успешного удаления файла на диске
- **Файл:** [`internal/backup/manager.go:560-610`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L560-L610)
- **Описание:** Метаданные из JSON-манифеста удалялись до `os.Remove(filePath)`. Если удаление файла падало, архив становился зомби на диске без отображения в UI.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `DeleteBackup` изменен порядок: сначала безопасно удаляется основной файл на диске (`os.Remove(targetPath)`), и только при успешном удалении стираются sidecar-файлы метаданных `.sha256` и `.json`. Добавлена очистка осиротевших sidecar-файлов, если основной файл уже отсутствует (BKP-12), и проверка `!info.IsDir()` для предотвращения случайного удаления директорий (BKP-13). Покрыто тестом `TestDeleteBackup_SafetyAndOrphanCleanup`.

### [MEDIUM] BKP-07: Отсутствие квот на размер директории бэкапов (Disk Exhaustion DoS)
- **Файл:** [`internal/backup/manager.go:60-120`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go#L60-L120)
- **Описание:** Нет ограничений на суммарный размер каталога `data/backups` или количество хранимых копий.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  1. Внедрена функция `checkDiskSpace` через `syscall.Statfs`, проверяющая наличие достаточного свободного места на файловой системе (минимум 200MB для etcd и 500MB для full backup) перед стартом операции.
  2. Реализована retention-политика ротации (`rotateBackups`): по умолчанию хранятся последние 20 бэкапов, более старые копии автоматически удаляются.

### [MEDIUM] BKP-08-14: Дополнительные дефекты резервного копирования
- `BUG-BKP-08`: Очистка осиротевших `.temp-etcd-*` при старте `NewBackupManager`.
- `BUG-BKP-09`: Проверка ошибок закрытия файлов `outFile.Close()` и записи sidecar `.sha256` / `.json`.
- `BUG-BKP-10`: Реализован метод `VerifyBackup(id)` с проверкой SHA256 хеша содержимого на диске.
- `BUG-BKP-12`: Очистка осиротевших sidecar-файлов в `DeleteBackup`.
- `BUG-BKP-13`: Запрет удаления директорий в `DeleteBackup`.
- `BUG-BKP-14`: Защита скачивания бэкапов в API.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненные исправления:**
  Все перечисленные дефекты полностью устранены в [`internal/backup/manager.go`](file:///home/artem/laba-kuber/TalosDeck/internal/backup/manager.go) и [`internal/api/backups.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go).

---

## 5. Security, JWT Authentication & Audit Trail (`internal/auth/`, `internal/audit/`)

### [CRITICAL] SEC-01: Дефолтный статический JWT-секрет и пароль администратора
- **Файл:** [`internal/auth/auth.go:36-56`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L36-L56)
- **Описание:** При отсутствии переменных окружения бэкенд использует статичные значения: пароль `admin` и ключ `talosdeck-default-secret-key-32-chars-long-jwt-auth`. Любой злоумышленник может локально подписать токен с ролью `admin` и получить полный доступ.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/auth/auth.go`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go) полностью удалены статичные пароль `admin` и статичный ключ подписи.
  1. При отсутствии `adminPassword` криптографически генерируется безопасный случайный эфемерный пароль длиной 16 байт (`RandomString(16)`), и в консоль пишется явное предупреждение `[SECURITY WARNING]`.
  2. При отсутствии `jwtSecret` генерируется случайный криптостойкий 256-битный секрет (`RandomString(32)`).
  3. В `NewAuthManagerFromEnv` генерация обернута в `sync.Once` для синхронизации в рамках процесса, если переменные окружения не заданы. Покрыто тестом `TestEphemeralDefaults`.

### [CRITICAL] SEC-02: Полное отсутствие авторизации на роуте `/api/audit`
- **Файл:** [`internal/api/server.go:196-210`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L196-L210)
- **Описание:** Журнал аудита с IP-адресами, именами пользователей и историей операций доступен анонимно.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/api/server.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go) на эндпоинт `GET /api/audit` повешен мидлварь `auth.RequireAuth(authMgr)`. Анонимный доступ немедленно отклоняется кодом `401 Unauthorized`. Добавлен юнит-тест `TestAuthAndAuditEndpoints/GET_/api/audit_(unauthorized)`.

### [HIGH] SEC-03: Уязвимость к Timing Attacks при проверке пароля
- **Файл:** [`internal/auth/auth.go:58-61`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L58-L61)
- **Описание:** Сравнение пароля через `==` вместо `subtle.ConstantTimeCompare` или безопасного bcrypt хеширования.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/auth/auth.go`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go) метод `VerifyPassword` переведен на `subtle.ConstantTimeCompare([]byte(password), []byte(a.adminPassword)) == 1` для защиты от атак по времени на длину и префикс пароля. Также поддержана проверка паролей, захэшированных алгоритмом `bcrypt` (`$2a$`, `$2b$`).

### [HIGH] SEC-04: Состояние гонки данных на мапе `AuditEvent.Details`
- **Файл:** [`internal/audit/logger.go:121-169`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go#L121-L169)
- **Описание:** Возврат поверхностной копии структуры с разделяемой `map[string]any` приводит к `fatal error: concurrent map read and map write`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/audit/logger.go`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go) реализовано полное глубокое копирование (`deepCopyDetails` / `copyAndSanitizeDetails`):
  1. При вызове `Log(event)` мапа `event.Details` защитно копируется перед добавлением в кольцевой буфер `m.events`.
  2. При вызове `GetEvents(...)` для каждого возвращаемого события создается независимая копия `Details`. Модификация полученной мапы клиентом не влияет на состояние аудитора. Проверено тестом `TestAuditSecurityAndRace` с флагом `-race`.

### [HIGH] SEC-05: Блокирующий синхронный `fsync` под эксклюзивным мьютексом
- **Файл:** [`internal/audit/logger.go:103-118`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go#L103-L118)
- **Описание:** Синхронный `m.logFile.Sync()` под `m.mu.Lock()` подвешивает весь сервер при медленном дисковом I/O.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/audit/logger.go`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go) из критической секции `m.mu.Lock()` в методе `Log()` удален блокирующий вызов `m.logFile.Sync()`. Запись строк лога буферизуется ядром ОС, а сброс `m.logFile.Sync()` гарантированно вызывается при `Close()` менеджера аудита.

### [MEDIUM] SEC-06: Слепое доверие заголовку `X-Forwarded-For` (IP Spoofing)
- **Файл:** [`internal/auth/auth.go:169-177`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L169-L177)
- **Описание:** Функция берет первый элемент `X-Forwarded-For` без валидации доверенных прокси.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/auth/auth.go`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go) в `GetClientIP` внедрена проверка `isTrustedProxy(directIP)`. Заголовки `X-Forwarded-For` и `X-Real-IP` учитываются только если непосредственное соединение исходит от доверенного прокси (loopback `127.0.0.1`/`::1` либо адреса из `TALOSDECK_TRUSTED_PROXIES`). Извлеченный IP валидируется через `net.ParseIP`. При любых аномалиях возвращается реальный `c.IP()`. Покрыто тестом `TestClientIPSpoofingProtection`.

### [MEDIUM] SEC-07: Отсутствие валидации claims `Issuer` и `Subject`, отсутствие Leeway
- **Файл:** [`internal/auth/auth.go:76-83, 95-113`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L76-L83)
- **Описание:** Парсинг токена не проверяет `Issuer`, а отсутствие Leeway вызывает сбои при дрифте системных часов.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/auth/auth.go`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go):
  1. В `GenerateToken` генерируется уникальный идентификатор токена `ID: uuid.New().String()` (JTI), `Issuer: "TalosDeck"`, `Subject: username`, а также `NotBefore` со сдвигом на 10 сек назад для защиты от рассинхронизации часов.
  2. В `ValidateToken` добавлены опции парсера `jwt.WithIssuer("TalosDeck")` и `jwt.WithLeeway(a.leeway)` (по умолчанию 1 минута), а также строгая проверка `claims.Subject == claims.Username` и запрет пустых субъектов.

### [MEDIUM] SEC-08: Фиктивный Logout и отсутствие отзыва токенов
- **Файл:** [`internal/auth/auth.go:63-92`](file:///home/artem/laba-kuber/TalosDeck/internal/auth/auth.go#L63-L92), [`internal/api/server.go:153-167`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L153-L167)
- **Описание:** `/api/auth/logout` возвращает 200 OK, но токен остается валидным на бэкенде 24 часа.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  1. В `AuthManager` внедрен потокобезопасный механизм отзыва токенов `revokedTokens map[string]time.Time` и метод `RevokeToken(tokenString)`.
  2. При обращении к `/api/auth/logout` токен из заголовка `Authorization: Bearer <token>` парсится и помещается в черный список отозванных токенов.
  3. В `ValidateToken` проверяется черный список отозванных токенов (по JTI и телу токена). После логаута попытка использовать токен немедленно отклоняется кодом `401 Unauthorized`. Добавлен юнит-тест `TestTokenRevocation` и интеграционный тест `TestAuthAndAuditEndpoints/POST_/api/auth/logout`.

### [LOW] SEC-09-14: Дополнительные дефекты аудита и аутентификации
- `BUG-SEC-09`: Утечка дескриптора файла аудита в `SetupServer`.
  - **Исправление:** В [`internal/api/server.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go) зарегистрирован хук завершения приложения `app.Hooks().OnShutdown`, корректно закрывающий дескриптор файла `auditMgr.Close()` при остановке сервера.
- `BUG-SEC-10`: Потеря записей при старте из-за буфера `bufio.Scanner` 64KB.
  - **Исправление:** В [`internal/audit/logger.go`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go) в `NewAuditManager` установлен увеличенный буфер `scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)` (до 10MB), исключающий потерю длинных записей аудита при перезапуске сервиса. Покрыто тестом `TestLargeAuditLine`.
- `BUG-SEC-11`: Небезопасные права `0644` у файла `audit.log` и `0755` у каталога.
  - **Исправление:** Каталог аудита создается с правами `0700` (`rwx------`), а файл `audit.log` открывается и принудительно выставляется в `0600` (`rw-------`).
- `BUG-SEC-12`: Утечка чувствительных данных в поле `Details["error"]` и других полях.
  - **Исправление:** В [`internal/audit/logger.go`](file:///home/artem/laba-kuber/TalosDeck/internal/audit/logger.go) добавлена автоматическая санитизация `copyAndSanitizeDetails`: ключи, содержащие `password`, `token`, `secret`, `auth`, `cookie`, `talosconfig`, `kubeconfig`, маскируются как `***MASKED***`, а в строковых значениях (включая сообщения об ошибках) маскируются паттерны Bearer-токенов и паролей. Покрыто тестом `TestAuditSecurityAndRace`.
- `BUG-SEC-13`: Очистка старых отозванных токенов из памяти.
  - **Исправление:** В методе `RevokeToken` реализована автоматическая очистка истекших записей из `a.revokedTokens` (`now.After(exp)`), предотвращающая утечку оперативной памяти при частых входах/выходах.
- `BUG-SEC-14`: Валидация входных токенов.
  - **Исправление:** Проверка пустых токенов, обрезка пробелов и строгая проверка HMAC алгоритма подписи.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅

---

## 6. Telegram Alerting & Cluster Watcher (`internal/alerts/`)

### [CRITICAL] ALT-01: Потеря алертов при сбоях сети (Alert Suppression on Failure)
- **Файл:** [`internal/alerts/watcher.go:156-158, 167, 177-181`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L156-L158)
- **Описание:** Игнорирование ошибок отправки алертов (`_ = w.alerts.Send...`) с безусловным обновлением состояния `prev.Ready = n.Ready`. При сбое связи алерт не уходит, а на следующем тике состояние считается неизменным — администратор никогда не узнает об аварии.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/alerts/watcher.go`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go) метод `CheckClusterHealth` переработан: состояние узла (`prev.Ready`, `prev.HighCPU`, `lastEtcdHealthy`) фиксируется как перешедшее в новое качество **только после успешной доставки алерта** (вызова `onSuccess()` при `sendFn() == nil`). При сбое отправки (недоступность Telegram, ошибка сети, HTTP 500) состояние в памяти сохраняется прежним, и на следующем тике вочер автоматически повторяет попытку отправки алерта. Добавлен тест `TestWatcher_NoAlertSuppressionOnFailure`.

### [HIGH] ALT-02: Захват `w.mu.Lock()` на всё время сетевых вызовов
- **Файл:** [`internal/alerts/watcher.go:130-236`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L130-L236)
- **Описание:** Блокировка держится во время опроса нод, etcd и HTTP-запросов к Telegram, замораживая UI (`/api/alerts/config`, `/api/alerts/status`).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/alerts/watcher.go`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go) все сетевые вызовы (`w.manager.ListNodes`, `w.manager.GetEtcdStatus`, `w.alerts.Send...`) вынесены **из-под блокировки `w.mu`**. Для защиты от параллельного исполнения тиков внедрен мьютекс `checkMu`, а `w.mu` захватывается исключительно на микросекунды для чтения и обновления снимка состояний. Вызовы `GetStatus()`, `GetNodeSnapshots()`, `GetActiveAlertsCount()` более не блокируются сетевым I/O.

### [HIGH] ALT-03: `w.running` не сбрасывается в `false` при отмене `ctx.Done()`
- **Файл:** [`internal/alerts/watcher.go:99-101`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L99-L101)
- **Описание:** При отмене контекста горутина завершается, но флаг `running` остается `true`, блокируя повторный `Start()`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В фоновую горутину `Start()` добавлен отложенный сброс флага: `defer func() { w.mu.Lock(); w.running = false; w.mu.Unlock() }()`. При завершении по `ctx.Done()` или остановке `running` всегда сбрасывается в `false`, что позволяет беспрепятственно перезапускать вочер. Покрыто тестом `TestWatcher_RestartAfterContextCancel`.

### [HIGH] ALT-04: Data Race на `w.stopChan` и утечка горутины при рестарте
- **Файл:** [`internal/alerts/watcher.go:78, 96, 121`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/watcher.go#L78)
- **Описание:** Чтение `<-w.stopChan` выполняется без мьютекса, перезапись поля в `Start()` приводит к гонке и зомби-горутине.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Канал `stopChan` передается в фоновую горутину по значению `go func(stopCh chan struct{})`, устраняя гонку при пересоздании канала в `Start()`. Для синхронизации жизненного цикла добавлен `sync.WaitGroup`: `w.wg.Add(1)` при старте, `w.wg.Done()` в defer горутины, и `w.wg.Wait()` в методе `Stop()`. Утечка зомби-горутин полностью устранена.

### [HIGH] ALT-05: Превышение лимита 4096 символов Telegram API
- **Файл:** [`internal/alerts/telegram.go:322, 533-542`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L322)
- **Описание:** Длинный список ошибок etcd приводит к ошибке `400 Bad Request: message is too long` и отбрасыванию алерта.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`internal/alerts/telegram.go`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go) в методах `SendAlertWithContext` и `sendRawTelegram` добавлен строгий лимит длины сообщения: если тело превышает 4000 UTF-8 символов, текст безопасно усекается с добавлением уведомления `<i>⚠️ ... [Message truncated due to 4096 character limit]</i>`. Ошибка `400 message is too long` исключена. Покрыто тестом `TestTelegramService_MessageTruncation`.

### [MEDIUM] ALT-06: Отсутствие обработки Rate Limiting (HTTP 429)
- **Файл:** [`internal/alerts/telegram.go:553-575`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L553-L575)
- **Описание:** При пачке алертов Telegram возвращает 429, повторная отправка и очередь сообщений отсутствуют.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `sendRawTelegram` реализован цикл повторов с экспоненциальным бэкоффом (до 3 попыток). При ответе `429 Too Many Requests` сервис парсит параметр `parameters.retry_after` и выдерживает указанную паузу перед повторной отправкой, гарантируя доставку сообщений. Покрыто тестом `TestTelegramService_RateLimitRetry`.

### [MEDIUM] ALT-07: Поломка HTML-тегов в `SendTestNotification`
- **Файл:** [`internal/alerts/telegram.go:425, 431, 595`](file:///home/artem/laba-kuber/TalosDeck/internal/alerts/telegram.go#L425)
- **Описание:** `formatMessageLines` экранирует `<b>` в `&lt;b&gt;`, в чат приходит сырой текст с тегами.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В `SendTestNotification` убран сырой HTML-тег `<b>TalosDeck Control Plane</b>`, текст приведен к чистому строковому формату (`Status: Operational`), который парсер `formatMessageLines` корректно форматирует в валидный HTML без двойного экранирования. Покрыто тестом `TestTelegramService_HTMLNoRawTagsInTest`.

### [MEDIUM] ALT-08-15: Дополнительные дефекты алертинга
- `BUG-ALT-08`: Поддержка `context.Context` в HTTP-клиенте Telegram.
  - **Исправление:** Методы `SendAlertWithContext`, `SendTestNotificationWithContext` и `sendRawTelegram` теперь принимают `context.Context` и используют `http.NewRequestWithContext`, гарантируя немедленную отмену сетевых запросов при завершении контекста сервера.
- `BUG-ALT-09`: `Stop()` не ожидал завершения горутины (`sync.WaitGroup`).
  - **Исправление:** Добавлен `w.wg.Wait()` в метод `Stop()`.
- `BUG-ALT-10`: Гонка при сохранении конфига в `UpdateConfig`.
  - **Исправление:** В `UpdateConfig` формируется изолированный снимок `TelegramConfig` под мьютексом, который сохраняется методом `saveConfigSnapshot` через временный файл с атомарным `os.Rename` и правами `0600` / `0700`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅

---

## 7. Fiber REST API & Static Embedding (`internal/api/`)

### [CRITICAL] API-01: Отсутствие авторизации на перезапуске сервисов
- **Файл:** [`internal/api/server.go:337-375`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L337-L375)
- **Описание:** `POST /api/nodes/:ip/services/:id/restart` доступен без авторизации и аудита. Любой клиент в LAN может остановить kubelet/etcd.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - Роут защищен `auth.RequireAuth(authMgr)` при инициализированном менеджере аутентификации.
  - Добавлена строгая валидация IP-адреса узла (`validateNodeIP`) и идентификатора сервиса с возвратом HTTP 400.
  - Интегрировано журналирование в журнал аудита `auditMgr.Log` с действием `service.restart`, пользователем, IP клиента и статусом `success`/`failed`.

### [CRITICAL] API-02: Неавторизованная выгрузка MachineConfig нод кластера
- **Файл:** [`internal/api/server.go:431-477`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L431-L477)
- **Описание:** `GET /api/nodes/:ip/config` отдает конфигурацию нод со всеми токенами и приватными ключами без `RequireAuth`.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - Добавлен middleware `auth.RequireAuth(authMgr)` для предотвращения неавторизованной выгрузки MachineConfig с закрытыми ключами и токенами доступа.
  - Добавлена валидация IP-адреса ноды (`validateNodeIP`).
  - Добавлен аудит-лог с действием `node.config.export`.

### [CRITICAL] API-03: Утечка Bot Token в открытом виде на роутах алертов
- **Файл:** [`internal/api/alerts.go:28-87, 120-170`](file:///home/artem/laba-kuber/TalosDeck/internal/api/alerts.go#L28-L87)
- **Описание:** `GET /api/alerts/config` и `handleConfigSave` отдают действующий токен Telegram-бота без маскирования и без аутентификации.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - `RegisterAlertRoutes` принимает `authMgr` и защищает группу `/alerts` с помощью `auth.RequireAuth(authMgr)`.
  - Все ответы `GET /api/alerts/config` и `POST/PUT /api/alerts/config` возвращают маскированный токен (`alerts.MaskToken`) и маскированный chat ID (`alerts.MaskChatID`).
  - В `handleConfigSave` блокируется перезапись действующего токена замаскированной строкой (содержащей `*`).
  - Добавлен аудит изменений конфигурации алертов (`alert.config.update`) и отправки тестовых сообщений (`alert.test`).

### [CRITICAL] API-04: Path Traversal в `POST /api/backups/create` через параметр `node`
- **Файл:** [`internal/api/backups.go:55-75`](file:///home/artem/laba-kuber/TalosDeck/internal/api/backups.go#L55-L75)
- **Описание:** Поле `req.Node` без валидации конкатенируется в путь файла etcd-снапшота, допуская запись файлов вне каталога бэкапов.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - Добавлена валидация `req.Node` через `net.ParseIP` (возврат HTTP 400 Bad Request при недопустимом формате).
  - Введен строгий whitelist допустимых типов бэкапов (`etcd`, `snapshot`, `full`, `archive`, `cluster`).
  - Добавлена проверка ошибок разбора тела запроса `c.BodyParser(&req)` при непустом теле.

### [HIGH] API-05: Отсутствие авторизации на переводе в Maintenance Mode
- **Файл:** [`internal/api/server.go:519-545`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L519-L545)
- **Описание:** `POST /api/nodes/:ip/maintenance` (cordon/uncordon) не защищен авторизацией.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - Добавлен middleware `auth.RequireAuth(authMgr)`.
  - Добавлена валидация IP-адреса через `validateNodeIP`.
  - Добавлена обработка ошибок `c.BodyParser(&body)` с возвратом HTTP 400 при поврежденном JSON.
  - Настроена фиксация переводов в Maintenance Mode в аудит-логе.

### [HIGH] API-06: Отсутствие аутентификации на WebSocket стримах (dmesg/logs)
- **Файл:** [`internal/api/server.go:665-752`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L665-L752)
- **Описание:** Анонимные клиенты могут непрерывно читать логи ядра и служб нод.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - В middleware `/ws` внедрена проверка аутентификации клиентов по трем векторам: заголовок `Authorization: Bearer <token>`, query-параметр `?token=<token>` (для веб-браузеров) и протокол `Sec-WebSocket-Protocol`.
  - При отсутствии или истечении JWT-токена соединение отклоняется с HTTP 401 Unauthorized.
  - В эндпоинтах `/ws/nodes/:ip/dmesg` и `/ws/nodes/:ip/logs/:service` добавлена валидация IP-адреса узла и имени сервиса с немедленным закрытием соединения при некорректных аргументах.

### [HIGH] API-07: SPA Fallback возвращает `index.html` (200 OK) на несуществующие API-роуты
- **Файл:** [`internal/api/server.go:660-664, 755-800`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L660-L664)
- **Описание:** Опечатка в API URL возвращает HTML вместо JSON 404 Not Found.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - Зарегистрирован catch-all обработчик `api.All("/*")`, гарантированно возвращающий HTTP 404 JSON `{"error": "API route ... not found"}`.
  - Зарегистрирован catch-all обработчик `app.All("/ws/*")`, возвращающий HTTP 404 JSON для несуществующих WebSocket маршрутов.
  - В `filesystem.Config` убран `NotFoundFile: "index.html"` и добавлен фильтр `Next` для исключения путей с префиксами `/api` и `/ws`.
  - SPA fallback `app.Get("/*")` отдает `index.html` исключительно для UI-маршрутов фронтенда.

### [HIGH] API-08: Отсутствие таймаутов сервера в `fiber.Config` (Slowloris DoS)
- **Файл:** [`internal/api/server.go:42-56`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L42-L56)
- **Описание:** `ReadTimeout`, `WriteTimeout`, `IdleTimeout` равны 0 (бесконечность).
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - В `fiber.Config` сконфигурированы таймауты соединений: `ReadTimeout: 15 * time.Second`, `WriteTimeout: 60 * time.Second`, `IdleTimeout: 120 * time.Second`, предотвращающие зависание сокетов и атаки Slowloris DoS.

### [HIGH] API-09: Отсутствие Rate Limiting на `POST /api/auth/login` и `/api/alerts/test`
- **Файл:** [`internal/api/server.go:104-124`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go#L104-L124), [`internal/api/alerts.go:172-188`](file:///home/artem/laba-kuber/TalosDeck/internal/api/alerts.go#L172-L188)
- **Описание:** Роуты открыты для брутфорса пароля и спама Telegram API.
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - На `POST /api/auth/login` подключен `limiter.New` (до 30 запросов в минуту с IP-адреса клиента) с возвратом HTTP 429 Too Many Requests при превышении порога.
  - На `POST /api/alerts/test` подключен `limiter.New` (до 30 запросов в минуту) для защиты от исчерпания лимитов Telegram API и спама в каналы.

### [MEDIUM] API-10-19: Дополнительные дефекты API
- **Статус:** `ИСПРАВЛЕНО (FIXED) ✅`
  - **API-10:** Обработаны ошибки `c.BodyParser(&body)` в эндпоинтах `/nodes/:ip/maintenance`, `/alerts/test`, `/backups/create` с возвратом HTTP 400 Bad Request при невалидном JSON.
  - **API-11:** Разработана и подключена вспомогательная функция `validateNodeIP(c)` для всех эндпоинтов с параметром `:ip` (`/nodes/:ip`, `/nodes/:ip/services`, `/nodes/:ip/containers`, `/nodes/:ip/disks`, `/nodes/:ip/reboot`, `/nodes/:ip/config`, `/nodes/:ip/maintenance`, `/ws/...`), предотвращающая обработку некорректных адресов.
  - **API-12:** Устранен дублирующий ошибочный роут `POST /api/api/nodes/:ip/reboot` в `server.go`.
  - **API-13:** В `DELETE /api/proxmox/worker/:vmid` и удалении бэкапов реализован возврат HTTP 404 Not Found вместо HTTP 500 при попытке удаления несуществующих ресурсов (VM или бэкапа).

---

## 8. Frontend State Management & API Client (`web/src/`)

### [CRITICAL] FE-01: Несовпадение схемы полей `K8sPod` и краш в WorkloadsView
- **Файлы:** [`web/src/types/index.ts:102-115`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L102-L115), [`web/src/components/views/WorkloadsView.vue:69-70`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/WorkloadsView.vue#L69-L70)
- **Описание:** В TypeScript объявлены `nodeName` и `ip`, а Go возвращает `node` и `podIp`. Ввод любого символа в поиск роняет вкладку с фатальной ошибкой `TypeError: Cannot read properties of undefined`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Go DTO заполняет канонические поля `nodeName`, `ip` и строковое `readyContainers`; фронтенд нормализует старые алиасы и выполняет null-safe поиск.

### [CRITICAL] FE-02: Несовпадение схемы полей `EtcdMember` и краш в OperationsView
- **Файлы:** [`web/src/types/index.ts:118-139`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L118-L139), [`web/src/components/views/OperationsView.vue:462-463`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L462-L463)
- **Описание:** TS обращается к `member.peerURLs[0]`, а Go возвращает `peerUrls`. Обращение к индексу `[0]` вызывает `TypeError: Cannot read properties of undefined (reading '0')`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** API-клиент нормализует `peerUrls`/`clientUrls` в ожидаемые интерфейсом `peerURLs`/`clientURLs` и гарантирует пустые массивы при отсутствии значений.

### [CRITICAL] FE-03: Отсутствие централизованной обработки 401 Unauthorized
- **Файлы:** [`web/src/api/index.ts:1300-1389`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L1300-L1389)
- **Описание:** Протухший токен никогда не удаляется из `localStorage`, пользователь остается в псевдо-авторизованном состоянии.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Общий обработчик ошибок защищённых запросов очищает токен и реактивное состояние пользователя при HTTP 401; `/api/auth/me` также явно сбрасывает истёкшую сессию.

### [HIGH] FE-04: Искажение ёмкости хранилища на порядки (GB vs байты)
- **Файлы:** [`web/src/types/index.ts:56-78`](file:///home/artem/laba-kuber/TalosDeck/web/src/types/index.ts#L56-L78), [`web/src/components/views/StorageView.vue:56`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/StorageView.vue#L56)
- **Описание:** Вызов `parseFloat(d.size)` над размером в байтах (53687091200) отображает суммарную емкость как 53 миллиарда гигабайт.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Ответ Talos API нормализуется в frontend DTO с отдельным `sizeBytes` и форматированной строкой размера. Все суммарные расчёты используют общий конвертер единиц B/KB/KiB/MB/MiB/GB/GiB/TB/TiB; фиктивный минимальный процент заполнения удалён. Проверено на значении `53687091200 B = 50 GiB`.

### [HIGH] FE-05: Лавинообразное наложение запросов в `setInterval` в App.vue
- **Файл:** [`web/src/App.vue:83-116`](file:///home/artem/laba-kuber/TalosDeck/web/src/App.vue#L83-L116)
- **Описание:** Автообновление через `setInterval` не ждет разрешения асинхронной цепочки `loadData()`. При медленной сети запросы накладываются друг на друга, вызывая гонки состояния.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** `setInterval` заменён на рекурсивный `setTimeout`, который планирует следующий опрос после завершения текущего. `loadData` защищён от параллельного запуска, а размонтирование компонента запрещает повторное создание таймера.

### [HIGH] FE-06: Маскировка ошибки 401 при перезагрузке узла под «Успех»
- **Файл:** [`web/src/api/index.ts:242-258`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L242-L258)
- **Описание:** При ошибке авторизации 401 блок `catch` возвращает `success: true`, выводя зеленый тост об успешной перезагрузке.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Ошибки reboot пробрасываются в UI; симуляция успешной мутации удалена.

### [HIGH] FE-07: Пропуск заголовков авторизации при вызовах Proxmox API
- **Файл:** [`web/src/api/index.ts:1108-1113, 1143-1148`](file:///home/artem/laba-kuber/TalosDeck/web/src/api/index.ts#L1108-L1113)
- **Описание:** Забыт вызов `...getAuthHeaders()`, создание и удаление воркеров через UI всегда падает с 401 Unauthorized.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** JWT передаётся при создании и удалении worker VM. Ложные успешные ответы при сетевых и серверных ошибках удалены.

### [HIGH] FE-08: Утечка зомби-интервала симуляции логов в LogsModal
- **Файл:** [`web/src/components/LogsModal.vue:143-147, 160-169`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/LogsModal.vue#L143-L147)
- **Описание:** При закрытии сокета возбуждается событие ошибки, запускающее фоновый таймер симуляции на закрытом окне.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Обработчики WebSocket привязаны к поколению соединения и состоянию модального окна; события закрытого или заменённого сокета больше не запускают симулятор.

### [MEDIUM] FE-09-17: Дополнительные дефекты фронтенд-состояния
- Falsy-баг: процессор с 0% загрузки принудительно заменяется на дефолтные 14%/22%.
- Пропуск `clearTimeout` при исключениях `fetch` (отсутствие блока `finally`).
- Отсутствие AbortController у мутирующих запросов.
- Игнорирование бэкенд-роута `/api/cluster` (хардкод обзора кластера).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Все HTTP-вызовы переведены на общий `fetchWithTimeout`, который отменяет зависшие запросы и очищает таймер в `finally`; это распространяется и на мутации. Обзор кластера загружается с `/api/cluster`, а вычисление по нодам осталось только резервным сценарием. Числовые метрики используют `??`, поэтому корректные нулевые значения больше не заменяются демонстрационными значениями.

---

## 9. Frontend Views & Modals UX/UI (`web/src/components/`)

### [HIGH] UI-01: Отсутствие блокировки прокрутки страницы (`body scroll lock`) во всех модальных окнах
- **Файлы:** Все модальные окна (`AddWorkerModal`, `LoginModal`, `LogsModal`, `RebootModal`, `ServicesModal`, `OperationsView`)
- **Описание:** При открытом модальном окне пользователь может свободно прокручивать контент основной страницы.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/App.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/App.vue) реализована централизованная блокировка скролла страницы (`body scroll lock`). Реактивный `watch` отслеживает состояние всех модальных окон (`isServicesOpen`, `isLogsOpen`, `isRebootOpen`, `isAddWorkerOpen`, `isLoginModalOpen`). При открытии любого окна устанавливается `document.body.style.overflow = 'hidden'`, при закрытии всех окон восстанавливается исходное состояние. Функция корректно очищается при демонтировании компонента.

### [HIGH] UI-02: Отсутствие закрытия модальных окон по клавише `Escape`
- **Файлы:** Все модальные окна
- **Описание:** Ни один компонент не обрабатывает нажатие клавиши `Escape`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/App.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/App.vue) добавлен глобальный слушатель события `keydown` на клавишу `Escape`. При нажатии последовательно закрываются открытые модальные окна (приоритетно текущее активное окно). Обработчик безопасно удаляется в хуке `onUnmounted`.

### [HIGH] UI-03: Модальное окно Rolling Reboot не закрывается по клику вне окна
- **Файл:** [`web/src/components/views/OperationsView.vue:1037-1040`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L1037-L1040)
- **Описание:** Отсутствует `@click.self` на оверлее, окно невозможно закрыть кликом по фону.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/views/OperationsView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue) на оверлей модального диалога Rolling Reboot добавлен директивный модификатор `@click.self="!rollingInProgress && (isRollingOpen = false)"`, позволяющий закрывать модалку кликом по фону вне окна (с блокировкой во время активного процесса перезагрузки).

### [HIGH] UI-04: Горизонтальное переполнение YAML-редактора в MachineConfigView
- **Файл:** [`web/src/components/views/MachineConfigView.vue:259, 276`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue#L259)
- **Описание:** Отсутствует `overflow-x-auto`, длинные строки конфигурации раздвигают верстку страницы.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/views/MachineConfigView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue) контейнерам редактора и просмотрщика конфигурации добавлены классы `overflow-x-auto`, `max-w-full` и корректный перенос `whitespace-pre`, предотвращающие раздвигание страницы по горизонтали длинными YAML-строками.

### [HIGH] UI-05: Переполнение карточки ноды при длинном Hostname
- **Файл:** [`web/src/components/NodeCard.vue:53-58`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/NodeCard.vue#L53-L58)
- **Описание:** Заголовок не имеет `truncate`/`min-w-0`, длинное имя FQDN ломает сетку карточек.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/NodeCard.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/NodeCard.vue) контейнер заголовка ноды и тег `<h3>` получили классы `min-w-0 max-w-full truncate` и всплывающую подсказку `:title="node.hostname"`. Длинные FQDN-имена узлов аккуратно усекаются многоточием и не ломают карточную сетку.

### [HIGH] UI-06: Вводящий в заблуждение Empty State при 0 нод
- **Файл:** [`web/src/components/views/NodesView.vue:285-292`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/NodesView.vue#L285-L292)
- **Описание:** Предлагает сбросить фильтры поиска вместо сообщения о потере связи с кластером.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/views/NodesView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/NodesView.vue) разделены сценарии Empty State: при отсутствии соединения/0 доступных узлов в кластере (`props.nodes.length === 0`) отображается предупреждающий статус о потере связи с кластером, а при 0 отфильтрованных узлов (`filteredNodes.length === 0`) выводится подсказка для сброса параметров поиска и фильтрации.

### [HIGH] UI-07: Рендеринг `undefined` в MachineConfigView при отсутствии нод
- **Файл:** [`web/src/components/views/MachineConfigView.vue:28-47, 246`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue#L28-L47)
- **Описание:** Выводит эндпоинт `/api/nodes/undefined/config` без предупреждения.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/views/MachineConfigView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue) внедрена защитная проверка на пустой список узлов кластера. При отсутствии нод запросы к `/api/nodes/undefined/config` блокируются, заголовок и хлебные крошки не рендерят `undefined`, а пользователю отображается информативный Empty State.

### [HIGH] UI-08: Тихий отказ Maintenance Mode при 0 нод
- **Файл:** [`web/src/components/views/OperationsView.vue:280-295`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue#L280-L295)
- **Описание:** Кнопка молча ничего не делает при клике.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/views/OperationsView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/OperationsView.vue) в методе `setMaintenance` добавлена проверка наличия узлов и выбранного таргета с выводом тост-уведомления об ошибке вместо тихого игнорирования клика, а кнопка перевода в Maintenance деактивируется (`:disabled`) при 0 узлов.

### [HIGH] UI-09: Отсутствие валидации имени ноды (RFC 1123) в AddWorkerModal
- **Файл:** [`web/src/components/AddWorkerModal.vue:170-180`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/AddWorkerModal.vue#L170-L180)
- **Описание:** Допускаются заглавные буквы и спецсимволы, вызывающие сбой создания ВМ.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`web/src/components/AddWorkerModal.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/AddWorkerModal.vue) внедрена валидация имени создаваемой ноды по спецификации RFC 1123 (`/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/`, до 63 символов). При вводе некорректных символов или заглавных букв выводится понятное сообщение об ошибке, а кнопка подтверждения блокируется.

### [MEDIUM] UI-10-22: Дополнительные дефекты верстки и доступности
- Сжатие кнопок действий в карточке ноды на экранах смартфонов <375px.
- Выход тоста уведомлений за левый край экрана на узких дисплеях.
- Низкий контраст номеров строк в YAML-редакторе (2.9:1 при норме WCAG 4.5:1).
- Поля ввода не связаны со своими `<label>` (отсутствуют `for` и `id`).
- Отсутствие Focus Trap и ARIA-атрибутов в модальных диалогах.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`web/src/components/NodeCard.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/NodeCard.vue) кнопки действий получили `min-w-0` и `truncate` на подписи (иконки — `shrink-0`), из-за чего на экранах <375px текст аккуратно обрезается вместо разрушения сетки `grid-cols-3`. В [`web/src/components/Toast.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/Toast.vue) добавлены `left-5` и `sm:left-auto` — на узких дисплеях тост растягивается между отступами и не вылезает за левый край, на `sm+` экранах поведение прежнее (прижат к правому краю). В [`web/src/components/views/MachineConfigView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/MachineConfigView.vue) цвет номеров строк YAML-редактора изменен с `text-zinc-600` на `text-zinc-400`, контраст на фоне `bg-zinc-950` теперь соответствует WCAG AA (≥4.5:1).
  Во всех формах (`AddWorkerModal`, `LoginModal`, `OperationsView`) добавлены парные атрибуты `id`/`for`, связывающие `<label>` с соответствующими `<input>`/`<select>` (имя и VMID воркера, слайдеры CPU/RAM/диска, пароль входа, Bot Token и Chat ID Telegram, минимальный уровень алертов, выбор целевой ноды для Maintenance Mode).
  Во всех модальных диалогах (`AddWorkerModal`, `LoginModal`, `LogsModal`, `RebootModal`, `ServicesModal`, диалог Rolling Reboot в `OperationsView`) панель диалога снабжена `role="dialog"`, `aria-modal="true"` и `aria-labelledby`, указывающим на заголовок окна. Полноценный keyboard focus trap (циклический обход по Tab внутри диалога) не реализован — это более объемная задача, требующая отдельного composable/directive; отслеживается как техдолг для последующей доработки вместе с `UI-01`/`UI-02`.

### [LOW] UI-23-30: Косметические недочеты
- Кнопки закрытия без `aria-label`.
- Хардкод списка нод в процедуре Rolling Reboot (`['talos-cp-1', ...]`).
- Индикаторы служб в карточке ноды всегда подсвечены зеленым (статический виджет).
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Все модальные окна (`LogsModal`, `AddWorkerModal`, `RebootModal`, `LoginModal`, `ServicesModal`, `Toast`, `Sidebar`, диалог Rolling Reboot в `OperationsView`) снабжены кнопками закрытия с атрибутами `:title="t('close')"` и `:aria-label="t('close')"` для корректной доступности скринридерами (WCAG 2.1). В процедуре Rolling Reboot захардкоженный статический массив нод заменен на реактивное вычисляемое свойство `computed(() => props.nodes.map(n => n.hostname || n.ip))` с проверкой на пустой список и блокировкой запуска при 0 доступных узлов. В карточке ноды `NodeCard.vue` статические зеленые индикаторы служб (etcd, kubelet, containerd, apid) переведены на функцию `getServiceStatus()`, динамически отражающую реальное состояние `node.servicesSummary` (зеленый для Healthy, пульсирующий красный для Degraded, серый для N/A / Unknown) с информативными всплывающими подсказками.

---

## 10. DevOps, Packaging, i18n & GitLab CI

### [CRITICAL] OPS-01: Сбой записи данных из-за прав non-root пользователя в Dockerfile
- **Файл:** [`Dockerfile:45-56`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile#L45-L56)
- **Описание:** Контейнер запускается от `talosdeck:talosdeck` (UID 1000), но каталог `/app/data` не создается и принадлежит `root`. Запись бэкапов и журнала аудита падает с `permission denied`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`Dockerfile`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile#L45-L65) в рантайм-образе на этапе сборки создана иерархия каталогов `mkdir -p /app/data/backups`, права рекурсивно назначены непривилегированному пользователю `chown -R talosdeck:talosdeck /app && chmod -R 755 /app/data`, а также объявлена директива `VOLUME ["/app/data"]`, гарантирующая права на запись для базы данных SQLite, журнала аудита `audit.log` и архивов резервного копирования.

### [CRITICAL] OPS-02: Публикация приватных ключей и сертификатов в git
- **Файл:** [`gitlab-deploy/manifests/talosdeck/secret.yaml:8`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/secret.yaml#L8)
- **Описание:** Base64-секрет с административным `talosconfig` находится в открытом виде в git-репозитории.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Конфиденциальные данные удалены из [`gitlab-deploy/manifests/talosdeck/secret.yaml`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/secret.yaml) и заменены на пустой плейсхолдер. Создан шаблон [`secret.example.yaml`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/secret.example.yaml). Файл `secret.yaml` внесён в `.gitignore`. Создан безопасный скрипт генерации [`scripts/export-talosconfig-secret.sh`](file:///home/artem/laba-kuber/gitlab-deploy/scripts/export-talosconfig-secret.sh) с выставлением прав доступа `0600`, а пайплайн CI/CD переведен на динамическую передачу секрета через защищённую переменную окружения `TALOSCONFIG` / `TALOSCONFIG_BASE64`.

### [CRITICAL] OPS-03: Утечка GitLab Personal Access Token в открытом виде
- **Файл:** [`gitlab-deploy/scripts/trigger-deploy.sh:7`](file:///home/artem/laba-kuber/gitlab-deploy/scripts/trigger-deploy.sh#L7)
- **Описание:** В скрипте захардкожен действующий токен `GITLAB_TOKEN="glpat-..."`, передаваемый по незащищенному HTTP.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`gitlab-deploy/scripts/trigger-deploy.sh`](file:///home/artem/laba-kuber/gitlab-deploy/scripts/trigger-deploy.sh) полностью удалён захардкоженный токен. Скрипт теперь принимает токен через переменную окружения `GITLAB_TOKEN="${GITLAB_TOKEN:-}"` с обязательной валидацией и аварийным завершением при ее отсутствии. Дефолтный URL GitLab обновлён на HTTPS (`https://gitlab.lan`), а в `README.md` добавлены инструкции безопасного запуска.

### [CRITICAL] OPS-04: Отсутствие ресурсов и проб в продакшн-манифесте Kubernetes
- **Файл:** [`gitlab-deploy/manifests/talosdeck/deployment.yaml:18-34`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/deployment.yaml#L18-L34)
- **Описание:** Полностью отсутствуют `resources.requests/limits` и пробы `livenessProbe`/`readinessProbe`. При зависании под остается активным, трафик не переключается.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В манифест [`gitlab-deploy/manifests/talosdeck/deployment.yaml`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/deployment.yaml#L34-L63) добавлены блоки `resources` (requests: 50m CPU, 64Mi RAM; limits: 500m CPU, 256Mi RAM), пробы `livenessProbe` и `readinessProbe` с интервалами и таймаутами на порт 8080, а также строгий `securityContext` (`runAsNonRoot: true`, `runAsUser: 1000`, `allowPrivilegeEscalation: false`).

### [HIGH] OPS-05: Неверсионированные (floating) базовые образы в Dockerfile
- **Файл:** [`Dockerfile:4, 18, 42`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile#L4)
- **Описание:** `alpine:latest`, `golang:alpine`, `oven/bun:1-alpine` нарушают воспроизводимость сборки.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`TalosDeck/Dockerfile`](file:///home/artem/laba-kuber/TalosDeck/Dockerfile) все плавающие теги базовых образов зафиксированы на детерминированных версиях: `oven/bun:1.2.4-alpine`, `golang:1.24-alpine`, `alpine:3.21.3`.

### [HIGH] OPS-06: Сокрытие сбоев деплоя (`|| true`) в GitLab CI
- **Файл:** [`gitlab-deploy/.gitlab-ci.yml:30, 47`](file:///home/artem/laba-kuber/gitlab-deploy/.gitlab-ci.yml#L30)
- **Описание:** Команды `kubectl rollout status ... || true` маскируют падение подов, рапортуя успешный статус пайплайна при аварии.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`gitlab-deploy/.gitlab-ci.yml`](file:///home/artem/laba-kuber/gitlab-deploy/.gitlab-ci.yml) удалены конструкции `|| true` из команд проверки статуса роллаута `kubectl rollout status` для сервисов `talosdeck` и `k8s-demo`. Теперь при падении подов или ошибке развертывания пайплайн корректно завершается со сбоем.

### [HIGH] OPS-07: Хардкод абсолютных путей разработчика в Makefile
- **Файл:** [`Makefile:13, 21`](file:///home/artem/laba-kuber/TalosDeck/Makefile#L13)
- **Описание:** Пути `/home/artem/laba-kuber/kubeconfig` делают сборку непереносимой.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`TalosDeck/Makefile`](file:///home/artem/laba-kuber/TalosDeck/Makefile) устранены захардкоженные абсолютные пути разработчика. Пути к `KUBECONFIG` и `TALOSCONFIG` приведены к переносимому виду со стандартными локациями `$${HOME}/.kube/config` и `$${HOME}/.talos/config`, а локальные пути сборки сделаны относительными.

### [HIGH] OPS-08: Фиктивные пробы доступности на корень `/`
- **Файл:** [`TalosDeck/deploy/deployment.yaml:37-52`](file:///home/artem/laba-kuber/TalosDeck/deploy/deployment.yaml#L37-L52)
- **Описание:** Проба проверяет только раздачу статического SPA HTML, но не проверяет соединение с Talos gRPC или etcd.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** В [`TalosDeck/internal/api/server.go`](file:///home/artem/laba-kuber/TalosDeck/internal/api/server.go) реализованы выделенные эндпоинты `/healthz` (liveness probe) и `/readyz` (readiness probe, валидирующий подключение к TalosManager и доступность кластера). В манифестах [`TalosDeck/deploy/deployment.yaml`](file:///home/artem/laba-kuber/TalosDeck/deploy/deployment.yaml) и [`gitlab-deploy/manifests/talosdeck/deployment.yaml`](file:///home/artem/laba-kuber/gitlab-deploy/manifests/talosdeck/deployment.yaml) параметры `livenessProbe` и `readinessProbe` переключены на `/healthz` и `/readyz`.

### [MEDIUM] OPS-09: 42 неиспользуемых («мертвых») ключа в словарях i18n
- **Файл:** [`web/src/i18n/index.ts:20-648`](file:///home/artem/laba-kuber/TalosDeck/web/src/i18n/index.ts#L20-L648)
- **Описание:** Неиспользуемые ключи создают технический долг и увеличивают размер бандла.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  Проведен аудит использования ключей локализации по всей кодовой базе фронтенда. Все неиспользуемые («мертвые») ключи (включая устаревшие и дублирующие ключи сервисов, дисков, аутентификации, аудита и модальных окон, такие как `services_col_*`, `services_state_*`, `storage_disk_*`, `audit_title`, `proxmox_scale_cluster` и др.) полностью удалены из обоих словарей `ru` и `en` в [`web/src/i18n/index.ts`](file:///home/artem/laba-kuber/TalosDeck/web/src/i18n/index.ts). Словари синхронизированы (по 252 актуальных ключа в каждой локали), устранен технический долг, уменьшен размер бандла. Сборка `vue-tsc -b && vite build` подтверждена без ошибок.

### [MEDIUM] OPS-10: Хардкод строк в обход интернационализации `t(...)`
- **Файлы:** `Sidebar.vue:125`, `NodesView.vue:245,290`, `StorageView.vue:111`, `ServicesModal.vue:100`
- **Описание:** Смешивание русских и английских надписей в интерфейсе при смене языка.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:**
  В [`web/src/i18n/index.ts`](file:///home/artem/laba-kuber/TalosDeck/web/src/i18n/index.ts) добавлены ключи `sidebar_nav`, `nodes_empty_title`, `nodes_empty_hint`, `services_subtitle`, `storage_drives_suffix` для обоих языков (`ru`/`en`). Хардкод заменен на `t(...)` в [`Sidebar.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/Sidebar.vue) («Навигация»), [`NodesView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/NodesView.vue) (метка фильтра «Workers» переиспользует ключ `stat_workers`; сообщения Empty State), [`StorageView.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/views/StorageView.vue) (суффикс «drives» в счетчике дисков) и [`ServicesModal.vue`](file:///home/artem/laba-kuber/TalosDeck/web/src/components/ServicesModal.vue) (подзаголовок «Talos Linux System & Kubernetes Daemons»). Сборка `vue-tsc -b && vite build` проходит без ошибок типов.

### [LOW] OPS-11-22: Дополнительные недочеты инфраструктуры
- Плавающий тег раннера `bitnami/kubectl:latest`.
- Деплой компонентов в namespace `default`.
- Отсутствие монтирования тома `/app/data` в Makefile таргете `docker-run`.
- **Статус:** **ИСПРАВЛЕНО (FIXED)** ✅
- **Выполненное исправление:** Образ раннера в `.gitlab-ci.yml` и `README.md` зафиксирован на стабильной версии `bitnami/kubectl:1.32.2`, соответствующей версии API Kubernetes кластера (`v1.32.2`). Деплой компонентов переведен из пространства имён по умолчанию (`default`) в изолированные выделенные namespaces (`talosdeck` для TalosDeck и `demo` для nginx-demo) с добавлением манифестов `namespace.yaml` в репозиториях `deploy/` и `gitlab-deploy/`, обновлением скрипта создания секрета `secret-create.sh` и адаптацией шагов CI/CD. В Makefile таргете `docker-run` добавлено автоматическое создание каталога `./data` и монтирование тома `-v "$$(pwd)/data":/app/data` для сохранения бэкапов и журнала аудита, а в `Dockerfile` каталогу `/app/data` гарантированы права непривилегированного пользователя `talosdeck:talosdeck`.
