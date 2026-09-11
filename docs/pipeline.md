# lotsman: конвейер OpenAPI → MCP

Детальное описание того, как спецификация превращается в исполняемые MCP-инструменты. Дополняет спеку v1.1.3; предназначен для `docs/pipeline.md`. Каждый этап привязан к пакету из §7.3.

```text
байты ──▶ 0.доверие ──▶ 1.парсинг ──▶ 2.$ref ──▶ 3.нормализация(IR) ──▶ 4.каталог/tools
                                                        │
                                                        ▼
                                                 capability report

вызов ──▶ 5.валидация args ──▶ 6.сериализация HTTP ──▶ 7.auth/egress ──▶ 8.shaping ответа
```

Этапы 0–4 выполняются один раз при загрузке (и при hot reload — на кандидате). Этапы 5–8 — на каждый tool call.

---

## Этап 0. Загрузка и доверие (`internal/specsource`)

Спека — недоверенный ввод. До парсинга применяются лимиты: размер root-документа, время чтения, источник по `SpecFetchPolicy`.

Специфичная для YAML атака, которую надо снять именно здесь: **anchor/alias-бомба** (миллиард смехов) — 2 КБ YAML разворачиваются в гигабайты. Лимитируется не размер файла, а бюджет узлов/алиасов при разборе и общий таймаут parse. Проверяется fuzz-тестом с готовой бомбой в корпусе.

Выход этапа: `[]byte` + метаданные источника (URL/путь, digest sha256, время).

## Этап 1. Парсинг (`internal/openapi`, адаптер libopenapi)

1. Определить версию по полю `openapi`/`swagger` **до** полного разбора: ветки 3.0.x и 3.1.x пойдут через разную нормализацию схем (этап 3.4), Swagger 2.0 — в compatibility adapter (после ядра).
2. Разобрать через libopenapi, собрать все ошибки разом (не first-error): пользователь должен увидеть полный список проблем с JSON Pointer и line/column.
3. Здесь же зафиксировать `specDigest` — он попадёт в capability report и `lotsman_catalog_info`.

Правило адаптера: типы libopenapi не покидают этот пакет. Наружу выходит только IR (этап 3). Это позволит заменить парсер и переживать его breaking changes локально.

## Этап 2. Разрешение `$ref` (`internal/openapi` + `RefPolicy`)

- **Локальные** (`#/components/...`): разрешаются всегда, с лимитом глубины.
- **Файловые** (`./common.yaml#/...`): только внутри объявленного spec root; `..`, symlink-escape и абсолютные пути вне root — отказ (это чтение произвольных файлов чужой спекой).
- **Сетевые** (`https://...`): по умолчанию выключены; при включении — HTTPS, allowlist origins, лимиты на число документов/байты/время.
- **Циклы** (`Node.children → Node`): детектируются по пути разрешения. Стратегия — не инлайнить бесконечно, а оборвать на глубине N, заменив хвост на `true`-схему (принимает всё) с диагностикой `cyclic_schema_truncated`. Операция при этом становится partially supported: аргументы глубже N не валидируются полноценно — честно сказать это в report, а в strict-режиме не публиковать.

Важное следствие для валидации (этап 5): вместо жадного инлайна всех ссылок лучше сохранять `$ref` как ссылки внутри одного документа и отдавать валидатору бандл целиком — JSON Schema 2020-12 сам умеет `$ref`/`$defs`, а инлайн взрывает размер схем на спеках типа Stripe в разы.

## Этап 3. Нормализация в IR (`internal/openapi` → `internal/domain`)

Самый содержательный этап. Вход — AST парсера, выход — `[]domain.Operation` + диагностики.

### 3.1 Перечисление операций

Обход `paths` в **отсортированном** порядке (map в Go не упорядочен — а нам нужен детерминизм каталога), внутри пути — методы в фиксированном порядке. Для каждой пары (path, method):

- слить path-level и operation-level `parameters`: operation-level переопределяет по ключу `(name, in)`;
- вычислить effective `servers`: operation.servers → path.servers → root.servers (первый непустой уровень побеждает);
- вычислить effective `security`: operation.security (включая явный пустой `[]` = «без auth»!) → root.security. Пустой массив на операции — легальный способ сказать «этот эндпоинт публичный», его нельзя терять;
- нормализовать путь: `/users/{userId}/posts/{postId}` → шаблон + список path-параметров. Проверить, что каждый `{placeholder}` имеет параметр с `in: path` и `required: true` — реальные спеки это нарушают, тогда либо нормализация с warning, либо отказ `path_parameter_mismatch`.

### 3.2 Идентичность операции

Три имени с разными ролями:

- `operationKey` = `{namespace}:{METHOD}:{normalizedPath}` — внутренний стабильный ключ. Не зависит от operationId, потому что **в реальных спеках operationId бывают дубликатами или отсутствуют** (это одна из самых частых грязностей);
- `sourceOperationID` — что было в спеке, сохраняется для диагностик;
- `toolName` — что увидит MCP-клиент: operationId → snake_case → фильтр `[A-Za-z0-9_.-]` → обрезка до 64 с учётом суффикса коллизии. Коллизия решается коротким стабильным hash от operationKey, НЕ порядком обхода. Если operationId нет — генерация из метода и пути: `GET /projects/{id}/members` → `get_projects_members` (+hash при неоднозначности).

Тест на детерминизм: одна спека, два запуска, побайтно одинаковый каталог и digest.

### 3.3 Параметры: defaults, которые все забывают

У каждого параметра нормализуются `style`/`explode` с учётом того, что **дефолты зависят от location**:

| in | default style | default explode |
|---|---|---|
| path | simple | false |
| query | form | **true** |
| header | simple | false |
| cookie | form | **true** |

Классическая ошибка конвертеров — сериализовать `query` array с `explode=false` дефолтом (`?ids=1,2,3` вместо правильного `?ids=1&ids=2&ids=3`). Матрица «что поддержано» из §4.5 спеки применяется здесь: параметр с `style: deepObject` в M1 даёт операции статус rejected c `unsupported_parameter_style`, а не «как-нибудь сериализуем».

### 3.4 Схемы: OAS → JSON Schema 2020-12

Ветка **3.1**: схема уже почти честный 2020-12 диалект — переносится с минимальной чисткой.

Ветка **3.0** требует настоящей трансляции:

- `nullable: true` + `type: string` → `type: ["string", "null"]`;
- `exclusiveMinimum: true` (boolean-модификатор к `minimum`) → `exclusiveMinimum: <число>` (в 2020-12 это самостоятельное числовое ключевое слово);
- `example` (единичный) → `examples: [...]`;
- OAS-специфичные ключи (`xml`, `discriminator`, `externalDocs`) — вырезать из валидационной схемы, сохранить в IR-метаданных;
- `readOnly: true` поля **исключаются из input-схемы** (их присылает сервер, а не клиент), `writeOnly` — наоборот, остаются в input, но выпадают из ожиданий ответа;
- ключи, которых в 3.0 нет, но которые встречаются в дикой природе (`const`, `if/then`) — диагностика, не молчаливый пропуск.

`allOf`/`oneOf`/`anyOf` в 2020-12 легальны — переносятся как есть; но `discriminator` без понятного mapping'а — кандидат на partial. Recursive схемы — см. этап 2.

### 3.5 Security: OR снаружи, AND внутри

```yaml
security:
  - apiKey: []                # альтернатива 1
  - oauth: [read]             # альтернатива 2: ЛЮБАЯ из строк достаточна (OR)
  - apiKey: []
    signature: []             # альтернатива 3: обе схемы СРАЗУ (AND)
```

IR хранит это как `[]SecurityAlternative{Requirements []SecurityRequirement}`. Выбор альтернативы при вызове — по auth policy из конфига (какие профили настроены), детерминированно; несколько выполнимых без явного выбора = `ambiguous_security` → rejected в strict.

### 3.6 Effect и вердикт поддержки

Каждой операции присваивается `EffectDecision{Effect, Source, Confidence}` (метод → inferred; override/recipe → explicit) + прогон suspicious-verb сканера. Затем — **вердикт**: `supported` / `partially_supported` (с documented fallback) / `rejected` (с machine-readable reason). Вердикт — это выход capability report, и правило одно: сомнение = rejected. lotsman doesn't guess.

## Этап 4. Генерация инструментов (`internal/catalog`)

Для каждой supported-операции собирается MCP tool:

**Input schema** — всегда grouped (FR-22):

```json
{
  "type": "object",
  "properties": {
    "path":    { "type": "object", "properties": { "projectId": {...} }, "required": ["projectId"], "additionalProperties": false },
    "query":   { "type": "object", "properties": { "page": {...} }, "additionalProperties": false },
    "headers": { "type": "object", "properties": {}, "additionalProperties": false },
    "body":    { ...нормализованная схема requestBody... }
  },
  "required": ["path"],
  "additionalProperties": false
}
```

`additionalProperties: false` на каждом уровне — принципиально: неизвестный аргумент от модели должен быть ошибкой валидации, а не молча выкинутым мусором (и не контрабандой в query).

**Description**: приоритет — проверенный override → summary → усечённый description. Санитизация: срезать HTML, управляющие символы, нормализовать whitespace; бюджет per-tool и на каталог целиком. Текст спеки — недоверенный: он попадёт в контекст LLM, поэтому чистка здесь — не косметика, а защита от prompt injection через описания.

**Annotations** — консервативно из effect: `read` → readOnlyHint; `unknown`/`write` → потенциально destructive. Annotations — подсказки клиенту; policy живёт на сервере и от них не зависит.

**Каталог**: сортировка детерминированная, digest считается по сериализованному содержимому, `tools/list` строится один раз на snapshot. В search-режиме вместо N tools публикуются 5 мета-инструментов, а каталог уходит в поисковый индекс.

## Этапы 5–8. Runtime-путь вызова

### 5. Валидация аргументов (`internal/policy` + validator)

Сервер валидирует args против опубликованной схемы **сам**, даже если клиент уже валидировал (в search-режиме клиент вообще не видел схему). После схемы — внутренние проверки: required path-параметры присутствуют, значения скалярные там, где ожидаются скаляры. И policy: effect разрешён? операция в allowlist? approval получен?

### 6. Сериализация HTTP (`internal/requestbuild`)

Самая коварная часть. По каждому location:

- **path**: значение подставляется в шаблон **с percent-encoding всех reserved-символов**. `projectId = "123/../../admin"` обязан превратиться в `%2F`-encoded строку, а не в новый путь — иначе path traversal чужими руками. Санитизация не «запретить плохие символы», а «закодировать всё, что не unreserved»;
- **query**: `form` со взрывом по explode; `allowReserved: true` меняет набор кодируемого; повторяющиеся ключи легальны. Не использовать слепо `url.Values.Encode()` — он сортирует ключи и не знает про allowReserved;
- **header**: значения проверяются на CR/LF (инъекция заголовков через аргумент — `\r\nAuthorization: ...`), запрещённые для установки заголовки (Host, Content-Length, Authorization — тот ставит auth-слой) отфильтровываются;
- **cookie**: сериализация form, значения кодируются;
- **body**: JSON-маршалинг ровно того, что пришло в `body` (после валидации), `Content-Type` из выбранного media type.

URL собирается из effective servers (+ подстановка server variables с валидацией по enum) или `--base-url`; итоговый origin проверяется EgressPolicy **после** полной сборки и резолва DNS.

### 7. Auth + сеть (`internal/auth`, `internal/egress`)

Конвейер RoundTripper'ов: rate limit → auth (последним перед сетью, чтобы секрет не попал в логи промежуточных слоёв) → redirect guard (каждый hop заново через egress, sensitive headers не пересекают origin) → лимит времени и байтов.

### 8. Shaping ответа (`internal/response`)

- читать тело через `io.LimitReader` с жёстким пределом; при усечении — не отдавать битый JSON как JSON: результат помечается `truncated: true`, тело отдаётся как текст;
- заголовки — через allowlist (request-id, rate-limit-инфо); `Set-Cookie`/`Authorization` не возвращаются никогда;
- 2xx → structuredContent `{status, contentType, headers, body, truncated, receivedBytes}`; 4xx/5xx → `isError: true` с ограниченным телом ошибки (модели полезно видеть текст ошибки API); транспортные/policy/validation ошибки различаются кодами;
- запись в audit: operationKey, effect, decision, origin без query, статус, длительность, размеры.

---

## Чек-лист ловушек (для тестового корпуса)

1. YAML anchor-бомба → лимит узлов (этап 0).
2. Дубликаты operationId → operationKey + hash-суффикс (3.2).
3. `{placeholder}` без объявленного path-параметра → mismatch (3.1).
4. Пустой `security: []` на операции = публичный эндпоинт, не «унаследовать глобальный» (3.1).
5. Query array с дефолтным explode=true (3.3).
6. `nullable` и boolean-`exclusiveMinimum` в OAS 3.0 (3.4).
7. `readOnly`-поля в input-схеме (3.4).
8. Циклический `$ref` → усечение + partial (2).
9. Path traversal через path-параметр → полный percent-encoding (6).
10. CRLF в header-параметре (6).
11. Усечённый JSON, выданный как валидный (8).
12. Относительный `servers: [{url: /api/v2}]` — резолвится от origin источника спеки только если источник URL; для файла — требует `--base-url` (6).
13. Недетерминированный порядок map при обходе paths → сортировка всего (3.1).
14. Секрет в query-параметре apiKey → редактирование в логах и audit (7–8).

Каждый пункт — минимум один golden/fuzz/integration тест. Корпус: GitLab, DigitalOcean, Kubernetes + синтетические спеки на каждую ловушку.
