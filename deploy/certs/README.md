# Корпоративные CA для образов Nexus

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

### Проверка после сборки

```bash
docker run --rm nexus-sender sh -c 'grep -c "BEGIN CERTIFICATE" /etc/ssl/certs/ca-certificates.crt'
# должно быть на 1 больше базового значения alpine (145 → 146)
ls /etc/ssl/certs | grep vozovoz   # ca-cert-vozovoz-issuing-ca.pem
```
