# Дополнительные CA для образов Nexus

Здесь лежат сертификаты удостоверяющих центров, которых **нет в базовом бандле
alpine**, но без которых Sender не может установить TLS-соединение с частью
узлов. Все файлы встраиваются в образ Sender'а
([deploy/docker/sender.Dockerfile](../docker/sender.Dockerfile)) через
`update-ca-certificates`. Файлы **дополняют** друг друга — ни один не заменяет
другой и не удаляется при добавлении нового.

| Файл | Зачем | Действует до |
| --- | --- | --- |
| `vozovoz-issuing-ca.crt` | внутренние домены `*.vz78.vozovoz.ru` | 28.03.2119 |
| `russian-trusted-root-ca.crt` | НУЦ Минцифры — боевой эквайринг Альфа-Банка | 27.02.2032 |

## `vozovoz-issuing-ca.crt` — «Vozovoz Issuing CA»

Промежуточный CA, которым подписаны сертификаты внутренних доменов
`*.vz78.vozovoz.ru`. Встраивается в образ Sender'а
([deploy/docker/sender.Dockerfile](../docker/sender.Dockerfile)), иначе исходящие
запросы к узлам на внутренних адресах падают с
`x509: certificate signed by unknown authority`.

**Отпечаток (SHA-1):** `1B:57:F6:98:06:5B:24:8E:2B:C1:07:03:5A:9B:60:62:40:62:43:89`
**Действует до:** 28.03.2119

### Почему именно промежуточный, а не корневой

Корневой «Vozovoz Root CA» **непригоден как CA**: у него отсутствует расширение
`basicConstraints` (нет `CA:TRUE`) и `keyUsage`. По RFC 5280 такой сертификат не
имеет права подписывать другие, и Go его отвергает **независимо от того, добавлен
он в хранилище или нет**:

```text
x509: invalid signature: parent certificate cannot sign this kind of certificate
```

То же самое подтверждает OpenSSL: `verify` даёт `error 24: invalid CA certificate`,
а `s_client` — `Verify return code: 26 (unsupported certificate purpose)`.

«Vozovoz Issuing CA» оформлен корректно (`CA:TRUE` critical + `Certificate Sign`,
`CRL Sign`), а Go принимает **любой** сертификат из пула как якорь доверия —
цепочка `leaf → Issuing CA` замыкается на нём, и до сломанного корня проверка не
доходит.

> **Не заменяй этот файл корневым CA «в порядке наведения порядка» — сломаешь TLS.**
> Правильное решение на стороне PKI: перевыпустить Root CA с
> `basicConstraints: CA:TRUE, critical` и `keyUsage: keyCertSign, cRLSign`.

### Как получить/обновить файл

Сертификат публичен — сервер отдаёт его в каждом TLS-рукопожатии:

```bash
echo | openssl s_client -connect geo2.vz78.vozovoz.ru:443 \
  -servername geo2.vz78.vozovoz.ru -showcerts 2>/dev/null
# взять второй сертификат в выводе (s: CN = Vozovoz Issuing CA)
```

**Обязательно сверь отпечаток** с копией из корпоративного хранилища, а не доверяй
соединению вслепую:

```powershell
Get-ChildItem Cert:\LocalMachine\CA | Where-Object { $_.Subject -like "*Issuing*" } |
  Select-Object Subject, Thumbprint
```

Файл должен быть в PEM с LF-переносами.

## `russian-trusted-root-ca.crt` — «Russian Trusted Root CA» (НУЦ Минцифры)

Корневой CA Национального удостоверяющего центра Минцифры. Им подписан **боевой**
эквайринг Альфа-Банка: 29.07.2026 ≈16:05 MSK `pay.alfabank.ru` переехал на цепочку
`leaf → Russian Trusted Sub CA → Russian Trusted Root CA`, российского НУЦ в
alpine-бандле нет, и узел `qr` встал с
`tls: failed to verify certificate: x509: certificate signed by unknown authority`
(следом открылся circuit breaker, и остальные запросы падали уже с
`circuit_breaker_open`).

**Отпечаток (SHA-256):** `D2:6D:2D:02:31:B7:C3:9F:92:CC:73:85:12:BA:54:10:35:19:E4:40:5D:68:B5:BD:70:3E:97:88:CA:8E:CF:31`
**Отпечаток (SHA-1):** `8F:F9:15:CC:AB:7B:C1:6F:8C:5C:80:99:D5:3E:0E:11:5B:3A:EC:2F`
**Действует:** 01.03.2022 — 27.02.2032

### Почему корневой, а не промежуточный (обратно случаю Vozovoz)

Здесь корень оформлен **корректно** — `basicConstraints: CA:TRUE, critical,
pathlen:4` и `keyUsage: Certificate Sign, CRL Sign`, — поэтому Go его принимает,
и правило «клади промежуточный» из соседнего раздела на НУЦ **не переносится**:
оно было вынужденной обходной мерой для сломанного «Vozovoz Root CA», а не общим
принципом.

Промежуточный Sub CA сознательно **не** кладём: сервер присылает его в
рукопожатии, а перевыпуск Sub'а не потребует пересборки образа. Дополнительный
довод — их несколько: на Госуслугах опубликован Sub с отпечатком SHA-256
`BB:BD:E2:10:…`, а `pay.alfabank.ru` отдаёт другой, `21:55:78:50:…`; оба подписаны
одним корнем.

> **Область доверия.** Этот CA добавляется в системный бандл контейнера, то есть
> действует для **всех** исходящих запросов Sender'а, а не только для Альфа-Банка.
> Точечного доверия «CA только для этого узла» в Nexus сейчас нет.

### Как получить/обновить файл НУЦ

Официальный источник — Госуслуги (тот же файл, что раздаёт инструкция Минцифры):

```bash
curl -sL -o russian-trusted-root-ca.crt \
  https://gu-st.ru/content/lending/russian_trusted_root_ca_pem.crt
```

**Сверь отпечаток с тем, что реально отдаёт целевой сервер** — так проверка не
зависит от доверия одному источнику:

```bash
echo | openssl s_client -connect pay.alfabank.ru:443 -servername pay.alfabank.ru \
  -showcerts 2>/dev/null | openssl x509 -noout -fingerprint -sha256
# и цепочка целиком:
openssl verify -CAfile russian-trusted-root-ca.crt -untrusted <sub.pem> <leaf.pem>  # → OK
```

Файл должен быть в PEM с LF-переносами (`curl` под Windows отдаёт CRLF —
нормализуй, иначе `update-ca-certificates` молча возьмёт файл, но diff будет шумным).

### Проверка после сборки

```bash
docker run --rm nexus-sender sh -c 'grep -c "BEGIN CERTIFICATE" /etc/ssl/certs/ca-certificates.crt'
# должно быть на 2 больше базового значения alpine (145 → 147)
ls /etc/ssl/certs | grep -E 'vozovoz|russian'
# ca-cert-vozovoz-issuing-ca.pem, ca-cert-russian-trusted-root-ca.pem
```

Функциональная проверка (busybox `wget` использует тот же системный бандл, что и Go):

```bash
docker run --rm nexus-sender sh -c 'wget -qS -O /dev/null https://pay.alfabank.ru/ 2>&1 | head -3'
# без CA: "ssl_client: ... untrusted certificate"; с CA — HTTP-ответ
```
