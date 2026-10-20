# Моди

**Мод** — це плагін Claude Code, у якого `hooks/hooks.json` перелічує `modules`: один
TypeScript-модуль *функціональних хуків* (виклики `$.…`, типізовані події) замість
shell-скриптів, які запускає звичайний хук. Функціональні хуки працюють усередині
сесії, тож мод бачить початок і кінець ходу, вимірює контекст, малює смугу над
промптом і відповідає на запит дозволу без окремого процесу. Їм потрібен Claude Code
**2.1.287 або новіший**; моди swarmery зібрано й перевірено на **2.1.296**, а в CI їх
перевіряють на закріпленій збірці.

Swarmery постачає два моди, обидва необов’язкові й обидва вимкнені, доки ви їх не ввімкнете:

| Плагін | Що це | Потребує |
|---|---|---|
| [`swarmery-link`](../../plugins/swarmery-link/README.md) | Звітує локальному демону swarmery про живий стан сесії. Необов’язкові опції додають Stop / надсилання промпту з дашборду, погодження у Swarmery, координацію рою між сесіями, смугу плану в терміналі та прогноз на кожен хід. | демон swarmery і його файл токена |
| [`guard-pack`](../../plugins/guard-pack/README.md) | Тримає файлові інструменти в межах коренів проєкту, вирізає значення, схожі на секрети, з результатів інструментів, перш ніж їх побачить Claude, віддає команди розгортання в production нативному діалогу запиту (а в режимі `auto`, де діалог не з’явиться, відмовляє в них) і може ховати агентів пакетів, яких проєкт не ввімкнув. | нічого: ні демона, ні `core` |

Протокол обміну між `swarmery-link` і демоном описано в
[`tools/swarmery/docs/mods-link-protocol.md`](../../tools/swarmery/docs/mods-link-protocol.md).

## Як увімкнути мод

Для кожного проєкту — у його `.claude/settings.json`:

```json
{
  "enabledPlugins": {
    "swarmery-link@swarmery": true,
    "guard-pack@swarmery": true
  }
}
```

Кожен можна ввімкнути окремо: вони не залежать один від одного. Або встановіть мод із
маркетплейсу командою `/plugin install swarmery-link@swarmery`. Щоб спробувати мод в
одній сесії з клону: `claude --plugin-dir plugins/swarmery-link`.

Увімкнений без жодної опції, `swarmery-link` — **лише сенсор**: він нічого не блокує,
не переписує й ні на що не відповідає. `guard-pack` за замовчуванням вмикає свої
захисти ізоляції, вирізання секретів і production (див. його `userConfig` нижче).

## Файл токена

`swarmery-link` звертається лише до `/api/link/*` на loopback-демоні, з bearer-токеном
із `~/.swarmery/link-token` (режим `0600`). Демон створює цей файл під час запуску.
`swarmery link token` друкує токен (і створює файл, якщо його немає), `--rotate`
замінює його, а `--print-path` друкує, де він лежить. Мод читає файл на кожному
`session.start` і тримає токен лише в пам’яті.

- **Файлу немає:** один рядок у лозі, `swarmery-link: no token, sensor off`, і жодного
  запиту до кінця сесії. Видалити файл — спосіб заглушити мод.
- **Замінений токен** мод підхоплює на наступному `401`: він один раз перечитує файл і
  один раз повторює запит.
- `guard-pack` не читає токена й не робить мережевих запитів.

## Опції (`userConfig`)

Опції задаються в меню конфігурації плагіна (`/plugin` → плагін → configure), яке
перезавантажує мод із новими значеннями, або в `pluginConfigs` у налаштуваннях: ключ
`<plugin>@<marketplace>`, значення під `options`:

```json
{
  "pluginConfigs": {
    "swarmery-link@swarmery": { "options": { "control": true, "planBand": true } }
  }
}
```

Кладіть цей блок у **користувацькі** налаштування (`~/.claude/settings.json`), а не в
`.claude/settings.json` чи `.claude/settings.local.json` проєкту. Claude Code читає
`pluginConfigs` лише з користувацьких налаштувань і з прапорця `--settings`; проєктну чи
локальну копію він ігнорує без попередження (виміряно на 2.1.283 для опцій плагінів;
[`claude-cli-config-channels.md`](../../tools/swarmery/docs/claude-cli-config-channels.md),
G1). Проєкт у маєтку `accounts-pack` отримує `pluginConfigs` кореня свого маєтку через
міст `--settings`, який будує цей пакет. Плагін, завантажений через `--plugin-dir`, має
ключ `<plugin>@inline`.
[`overlays/example/user-settings.snippet.json`](../../overlays/example/user-settings.snippet.json)
— той самий блок у вигляді файлу.

### swarmery-link

| Опція | За замовчуванням | Що робить |
|---|---|---|
| `daemonUrl` | `http://127.0.0.1:7777` | Адреса демона. Лише loopback `http` (`127.0.0.1`, `[::1]`, `localhost`, будь-який порт); будь-що інше відхиляється й береться значення за замовчуванням, тож токен ніколи не покидає машину. |
| `control` | `false` | Дашборд може зупинити поточний хід сесії й надсилати їй промпти. Один фоновий цикл тримає long-poll до демона за командами для цієї сесії. |
| `approvals` | `false` | Запит дозволу спершу йде в чергу погоджень Swarmery; на `AskUserQuestion` можна відповісти з дашборду. Shim хука `PermissionRequest` і нативний діалог перебирають справу на себе, щойно демон недоступний або не вирішує вчасно. |
| `swarm` | `false` | Повідомлення між сесіями потрапляють у звіт, кожен файл, який сесія починає редагувати, заявляється в реєстрі, спільному для її сесій, а рядок під відповіддю позначає файл, який тримає інша жива сесія. З увімкненим `control` дашборд може написати сесії. |
| `claimConflicts` | `off` | З увімкненим `swarm` визначає, що стається з `Edit`/`Write`/`MultiEdit`/`NotebookEdit` файлу, який тримає інша жива сесія. `off`: лише рядок під відповіддю. `ask`: нативний діалог питає, називаючи іншу сесію, у режимах `default`, `acceptEdits` і `plan`; у `auto`, `dontAsk` і `bypassPermissions`, де діалог не дійде до людини, запис відхиляється. `deny`: відхиляється в будь-якому режимі. Bash ніколи не перевіряється. Режим читається з `permission_mode` класичних подій хуків; доки жодна не надійшла, запис запитується. |
| `planBand` | `false` | Смуга над промптом показує фазу плану сесії та її відкриті критерії, `/phase` відкриває чекліст, питання в стилі інтерв’ю малюються карткою, а рядок під відповіддю позначає хід із правками, що схоже виходить за межі поточної фази. |
| `forecast` | `false` | Один виклик haiku на кожен набраний хід передбачає, чого торкнеться хід; демон порівнює прогноз із тим, що хід зробив (лише шляхи й числа). |

### guard-pack

| Опція | За замовчуванням | Що робить |
|---|---|---|
| `isolation` | `true` | Забороняє файлові інструменти, а також `rm`/`mv`/`cp`/перенаправлення/`cd` у Bash, на шляхах поза коренями проєкту. |
| `redaction` | `true` | Замінює значення, схожі на секрети, у результатах інструментів на `[REDACTED:<rule>]`, перш ніж їх побачить Claude. Сирий вивід може лишитися в локальному транскрипті. |
| `prodGuard` | `true` | Віддає команди розгортання в production нативному діалогу запиту, хоч би що казали правила дозволів. У режимах `auto`, `dontAsk` і `bypassPermissions`, де діалог не дійде до людини, натомість відмовляє в них. Режим читається з `permission_mode` класичних подій хуків. |
| `agentOffer` | `false` | Ховає агентів `<pack>:<agent>` пакетів, яких цей проєкт не ввімкнув. |
| `deferHeavyTools` | `false` | Ховає за ToolSearch MCP-інструменти з описом понад 2048 символів. |
| `extraRoots` | `""` | Додаткові теки через кому, які захист ізоляції вважає частиною проєкту. |

## Що показує `claude plugin validate`

`claude plugin validate --strict --json <plugin dir>` читає маніфест і код модуля так,
як це зробить рушій, і для кожного модуля перелічує хуки, які він реєструє, виклики `$`,
які він робить, і змінні середовища, які він читає. Він перелічує все, що згадано в коді,
незалежно від опцій: вимкнена опція реєструє менше хуків і робить менше викликів, ніж
показує таблиця.

| Плагін | Хуки | Виклики `$` | Читання env |
|---|---|---|---|
| `swarmery-link` | `session.start`, `turn.start`, `tool.call`, `tool.check`, `tool.call{tool=AskUserQuestion}`, `classic.SessionStart`, `classic.UserPromptSubmit`, `classic.PostToolUse`, `classic.PostToolUseFailure`, `session.measure`, `turn.complete`, `session.end`, `session.send`, `session.receive`, `ui.render{component=AbovePrompt}`, `ui.render{component=Pane, requestId=?}`, `ui.render{component=AskUserQuestion}`, `command.run{command=phase}` | `$.clock`, `$.command.register`, `$.env.get`, `$.fs.read`, `$.http.fetch`, `$.model.classify`, `$.model.complete`, `$.prompt.submit`, `$.session` (`cwd`, `id`, `send`, `usage`), `$.store`, `$.turn.abort`, `$.ui` | `HOME` |
| `guard-pack` | `session.start`, `tool.call`, `session.append`, `classic.SessionStart`, `classic.UserPromptSubmit`, `classic.PostToolUse`, `classic.PostToolUseFailure`, `tool.check`, `agent.offer`, `tool.describe` | `$.env.get`, `$.fs.read`, `$.session.cwd`, `$.session.root`, `$.settings.read`, `$.ui.notice`, `$.ui.status` | `AGENT_PROJECT`, `AGENT_WORKSPACE_ROOT`, `HOME`, `TMPDIR` |

Жоден не пише змінних середовища. Кожен блокувальний хук обох модів має `.catch`, який
передає подію далі без змін, тож хук, що впав, ніколи не блокує сесію. README кожного
плагіна пояснює, яка опція відповідає за який хук і виклик.

Дашборд запускає той самий валідатор: **Система → Моди** перелічує кожен встановлений мод
(кеш плагінів, а також `CLAUDE_CODE_PLUGIN_DIRS` з `env` налаштувань), повторно валідує
мод, коли змінюються файли його модуля, і показує його хуки, виклики й читання env зі
значками ризику. CI запускає `bash scripts/tests/mods-ci.test.sh`: строгу валідацію плюс
`claude plugin test` для кожного мода в цьому репозиторії.

## Як перевірити мод наживо на dev-демоні

`claude plugin test` проганяє мод на тестовому наборі. Щоб побачити, як він працює в
справжній сесії зі справжнім демоном і не зачепити демон, яким ви користуєтеся щодня,
запустіть другий демон із клону з власною базою даних і робочим простором:

```bash
make -C tools/swarmery build
SWARMERY_WTJANITOR=0 tools/swarmery/swarmery serve -port 7788 \
  -db <scratch>/dev.db -workspace-root <scratch>/workspace \
  -plugin-drift-interval 0
```

- `SWARMERY_WTJANITOR=0` вимикає прибиральника worktree, який інакше видалив би
  агентські worktree на машині.
- `-plugin-drift-interval 0` вимикає сканер розходження плагінів.
- Dev-демон використовує той самий `~/.swarmery/link-token`, тож мод автентифікується
  в ньому без додаткових кроків.

Спрямуйте мод на нього тимчасовим файлом налаштувань. Він вимикає встановлені копії,
щоб працював лише мод із клону, і задає опції під ключем `@inline`, який отримує плагін
із `--plugin-dir`:

```json
{
  "enabledPlugins": {
    "swarmery-link@swarmery": false,
    "guard-pack@swarmery": false
  },
  "pluginConfigs": {
    "swarmery-link@inline": {
      "options": { "daemonUrl": "http://127.0.0.1:7788", "control": true }
    }
  }
}
```

Потім запускайте кожну перевірку як headless-сесію в тимчасовому git-репозиторії, ніколи
не в клоні:

```bash
cd <scratch>/repo
claude -p --model haiku --plugin-dir <swarmery-repo>/plugins/swarmery-link \
  --settings <scratch>/settings.json "<prompt>"
```

Записуйте, що показала кожна перевірка (відповідь API, стан дашборду, рядок логу), у
phase-документ плану одразу, щойно вона завершиться, і лише тоді починайте наступну.
Наприкінці зупиніть dev-демон і кожну сесію, яку ви запустили.

## Де він деградує

Мод усюди необов’язковий. Демон і далі записує кожну сесію з її JSONL-транскрипту, а там,
де встановлено shim хука `PermissionRequest` (README, «Route permission prompts to the
dashboard»), погодження й далі доходять до дашборду через нього.

- **Немає демона або файлу токена.** `swarmery-link` мовчить: без токена немає жодного
  запиту, а демон, що перестав відповідати, коштує один рядок у лозі й повтор кожні 2 с.
  Жоден хук сенсора не чекає на демона; чекає лише `session.end`, щонайбільше 1,2 с. З
  увімкненим `approvals` кожне очікування обмежене, і справу перебирає власний вердикт
  рушія, shim або нативний діалог. `guard-pack` демона не використовує.
- **Headless (`claude -p`).** Спрацьовує кожен хук, який нічого не малює, тож сенсор,
  командний канал і погодження працюють і в headless-запуску. Нічого не малюється: немає
  смуги плану й картки інтерв’ю. `/phase` і рядок про відхилення працюють.
- **Панель чату VS Code.** Наживо не перевірено. Тестовий набір валідує дерево смуги для
  поверхні `vscode`, але чи завантажує панель мод — це перевірка для оператора
  ([`mods-format.md`](../../tools/swarmery/docs/mods-format.md), M5).
- **Claude Code, старіший за 2.1.287**, з’явився раніше за функціональні хуки. На такій
  збірці моди не пробували; оновіться, перш ніж вмикати мод.
- **Класифікатор auto-режиму.** `$.session.send` (повідомлення іншій сесії) із сесії, чий
  режим дозволів запускає класифікатор auto-режиму, може впасти з «classifier gave no
  verdict». Передайте повідомлення із сесії в іншому режимі дозволів.

## Як оновити мод

Підтягніть нову версію у встановлення рівня користувача:

```bash
claude plugin marketplace update swarmery
claude plugin update swarmery-link@swarmery --scope user
claude plugin update guard-pack@swarmery --scope user
```

Передавайте `--scope user`. Без нього CLI може натомість оновити встановлення рівня
проєкту, що належить якомусь іншому клону (помічено 2026-10-19). Нова версія завантажиться
в наступній сесії.

Якщо `claude plugin marketplace update swarmery` не спрацював, маркетплейс не оновився.
Команда наново клонує весь репозиторій у
`~/.claude/plugins/marketplaces/swarmery..clone` і може впасти з
`Failed to clone … swarmery..clone/.git`, з `Could not resolve host: github.com`
або зависнути. Код виходу при цьому буває 0, тож дивіться на повідомлення, а не на код.
Наявний клон — звичайний git-репозиторій, тож оновіть його самим git:

```bash
git -C ~/.claude/plugins/marketplaces/swarmery fetch --depth 1 origin main
git -C ~/.claude/plugins/marketplaces/swarmery reset --hard origin/main
claude plugin update swarmery-link@swarmery --scope user
claude plugin update guard-pack@swarmery --scope user
```

Ваших локальних змін у клоні немає, тож `reset` нічого не втратить. Якщо завислий
повторний клон досі працює (`pgrep -fl 'swarmery\.\.clone'`), спершу зупиніть його.

## Вимкнення

Задайте `"<plugin>@swarmery": false` в `enabledPlugins` або вимкніть мод у `/plugin`. Щоб
лишити `swarmery-link` увімкненим, але вимкнути опцію, скиньте її в меню конфігурації або
приберіть із `pluginConfigs`. Видалення `~/.swarmery/link-token` заглушує `swarmery-link`
в усіх проєктах одразу, доки демон знову не запише файл.
