# Сертификаты Talos и Kubernetes

TalosDeck помогает замечать приближение срока действия сертификатов. Обновление credentials выполняет администратор; мониторинг сам не выпускает сертификаты и не меняет CA.

## Что означает результат проверки

Текущее покрытие: встроенные CA и клиентские сертификаты сохранённых Talos/Kubernetes credentials, TLS-сертификат Kubernetes API endpoint и TLS-сертификаты Talos API по нодам. Внутренние dynamic certificate resources не читаются. Полной инвентаризации сертификатов etcd, kubelet, controller-manager и scheduler нет. Кнопки выпуска/ротации сертификатов в этой функции нет.

Различайте сертификат клиента из сохранённого `talosconfig`/`kubeconfig`, сертификат CA и сертификат сервера, предъявленный при TLS-соединении. Срок одного не определяет срок остальных.

- Даты сертификата из credentials относятся именно к копии, сохранённой в TalosDeck. Обновление файла на рабочем компьютере не обновляет эту копию.
- TLS-проверка endpoint видит сертификат конкретного соединения. За балансировщиком это может быть сертификат прокси или одной control-plane ноды; результат не подтверждает состояние всех реплик.
- Недоступный endpoint, отсутствующие права, неизвестный формат и отсутствующий клиентский сертификат — неполное покрытие. Их нельзя интерпретировать как отсутствие истекающих сертификатов. У token-based kubeconfig может вообще не быть клиентского X.509-сертификата.
- Проверка дат не доказывает доверие цепочке, соответствие имени endpoint или возможность авторизации. Для этого отдельно нужна успешная проверенная TLS/API-связь.

Сертификаты etcd peer/client, kubelet client/serving, controller-manager, scheduler и front-proxy требуют отдельного источника данных. Сертификат API endpoint не заменяет эту инвентаризацию. Объекты CSR показывают запросы и выданные сертификаты, но не гарантируют, что именно они сейчас используются процессом. Встроенное одобрение Kubernetes для kubelet client CSR не означает автоматического одобрения serving CSR. [Kubernetes TLS bootstrapping](https://kubernetes.io/docs/reference/access-authn-authz/kubelet-tls-bootstrapping/).

Пороги ниже — политика мониторинга TalosDeck, а не рекомендации upstream. Для клиентских сертификатов, CA и Kubernetes API предупреждение начинается при остатке не более 30 дней; при остатке не более 14 дней причина предупреждения уточняется. Не более 7 дней — критический статус.

Для серверного сертификата Talos API действуют отдельные пороги: не более 1 часа — предупреждение, не более 10 минут — критический статус; больший остаток считается нормальным. Эти сертификаты короткоживущие и автоматически ротируются, поэтому общий порог в 7 дней создавал бы ложные тревоги. Истёкший или ещё не действующий сертификат любой категории имеет критический статус.

Данные кэшируются на 5 минут; фоновая проверка выполняется примерно раз в 5 минут. Проверяйте время последнего наблюдения: это не непрерывная проверка TLS.

## Обновить admin talosconfig до истечения срока

Используйте действующий `os:admin` доступ и адрес control-plane ноды. Создавайте отдельный файл, сохраняя рабочий доступ до проверки:

```bash
umask 077
mkdir -p ./renewed-credentials

talosctl --talosconfig /secure/talosconfig \
  --endpoints CP_IP --nodes CP_IP \
  config new ./renewed-credentials/talosconfig \
  --roles os:admin --crt-ttl 8760h

talosctl --talosconfig ./renewed-credentials/talosconfig \
  --endpoints CP_IP --nodes CP_IP version
```

`CP_IP` замените реальным адресом. Явные endpoints исключают зависимость от локального default context. Проверьте endpoints в новом файле перед его использованием приложением. Команда выпускает новый клиентский доступ, а не продлевает существующий сертификат. [Talos CLI: config new](https://www.talos.dev/latest/reference/cli/#talosctl-config-new).

Если доступ уже истёк, онлайн-выпуск через него не сработает. Восстановление возможно из заранее сохранённого `secrets.yaml` либо control-plane MachineConfig с соответствующим CA private key. Выполняйте процедуру в защищённой среде по [официальной инструкции Talos PKI](https://docs.siderolabs.com/talos/v1.12/security/cert-management); эти материалы нельзя прикладывать к issue или журналу диагностики.

## Обновить kubeconfig

С действующим Talos admin-доступом получите новый файл без слияния с пользовательским kubeconfig:

```bash
umask 077
talosctl --talosconfig ./renewed-credentials/talosconfig \
  --endpoints CP_IP --nodes CP_IP \
  kubeconfig ./renewed-credentials/kubeconfig --merge=false

kubectl --kubeconfig ./renewed-credentials/kubeconfig \
  --request-timeout=15s get nodes
```

Загрузка через Talos выпускает новый клиентский сертификат; ранее скачанные копии остаются прежними. [Talos PKI](https://docs.siderolabs.com/talos/v1.12/security/cert-management). Срок выдаваемого admin kubeconfig задаёт `cluster.adminKubeconfig.certLifetime`; проверяйте фактический `NotAfter`, а не предполагаемый год. [Talos MachineConfig](https://www.talos.dev/v1.8/reference/configuration/v1alpha1/config/#adminkubeconfig).

В текущей версии нет API/UI замены сохранённых credentials; повторный импорт того же кластера отклоняется как дубликат. Замена внешнего файла не обновляет credentials в SQLite. Обновление подключения — отдельное ограничение lifecycle, а эта функция пока предоставляет мониторинг. Не редактируйте SQLite вручную и не удаляйте кластер ради обновления сертификата: его UUID связан с заданиями и историей. Успешный выпуск нового сертификата сам по себе не отзывает старый.

## Автоматическая ротация и CA

Talos управляет серверными сертификатами etcd, Kubernetes и Talos API. Документация отдельно указывает необходимость перезапуска kubelet минимум раз в год; reboot/upgrade также удовлетворяет этому условию. Это не повод немедленно перезагружать кластер: обслуживание планируют с учётом workloads и quorum. [Talos PKI](https://docs.siderolabs.com/talos/v1.12/security/cert-management).

Kubelet client rotation зависит от конфигурации и работоспособности CSR signing/approval. [Kubernetes certificate rotation](https://kubernetes.io/docs/tasks/tls/certificate-rotation/).

Ротация CA меняет доверие всего кластера и является отдельной операцией. Не используйте её для обычного renewal клиентского сертификата. `talosctl rotate-ca` требует собственного плана и резервных копий; Kubernetes API CA не охватывает автоматически все остальные PKI-компоненты. [Talos CLI: rotate-ca](https://www.talos.dev/latest/reference/cli/#talosctl-rotate-ca).

## Проверка без изменений инфраструктуры

Для сверки мониторинга достаточно разобрать в памяти публичные сертификаты выбранных контекстов и выполнить проверенные TLS-соединения к явно указанным endpoints. Сохраняйте только тип, fingerprint, даты, источник и результат проверки. Не сохраняйте PEM, private keys, tokens или исходные config-файлы в отчёте.

Для доступности используйте `talosctl version` и `kubectl get nodes` с явными путями конфигураций и timeout. `config new`, `kubeconfig`, `rotate-ca`, approve CSR и перезапуски для такой проверки не нужны. Ресурсы Talos с сертификатами могут также содержать private keys: сырой вывод `KubernetesDynamicCerts` не предназначен для UI, audit или support bundle.
