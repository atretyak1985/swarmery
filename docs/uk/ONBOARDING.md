# Підключення проєкту до swarmery

## Шлях однією командою

З кореня нового проєкту:

```bash
bash <swarmery-repo>/scripts/init.sh <project-slug> [pack ...]
# напр.  bash <swarmery-repo>/scripts/init.sh my-shop web-pack
```

Команда розгортає `.claude/settings.json` (маркетплейс + core + вибрані пакети + env + захисні заборони),
скелет `.claude/project.json` (заповніть TODO) і простір імен у робочому просторі. Потім відкрийте
нову сесію і прийміть запит довіри. Ідемпотентна — наявні файли ніколи не перезаписуються.

**Необов’язково, раз на машину:** зареєструйте маркетплейс на рівні користувача (`~/.claude/settings.json`
→ той самий блок `extraKnownMarketplaces`), щоб кожен проєкт на машині вже знав `swarmery`,
а налаштування проєкту скоротилися до `enabledPlugins` + `env`.

**Пакети:** `uav-pack` (дрони/телеметрія) · `iot-pack` (пристрої/BLE) · `web-pack` (SEO/i18n/CRO) · `infra-pack` (k8s/Helm/GitOps/GitLab-CI/Keycloak) ·
`lsp-pack` (семантична навігація кодом через Serena — ⚠️ **потребує бінарника `serena` на машині**:
`uv tool install serena-agent`; без нього кожна сесія логує невдалий запуск MCP. Перевизначення
`--project` для монорепозиторіїв описані в `plugins/lsp-pack/README.md`).

## Statusline (необов’язкове доповнення)

`init.sh` / `swarmery onboard` **не** встановлюють statusline — він вмикається лише явно,
тож повторний запуск init ніколи не повертає прив’язку чи розгорнуту копію, яку ви прибрали.
Щоб увімкнути його в проєкті:

```bash
# новий проєкт, бінарник центру керування в PATH — розгортає скрипти І прив’язує settings.json:
swarmery onboard <project-slug> --statusline-src <swarmery-repo>/plugins/core/statusline

# уже підключений проєкт (домерджує лише те, чого бракує, ідемпотентно):
swarmery attach --statusline-src <swarmery-repo>/plugins/core/statusline

# або повністю вручну:
mkdir -p .claude/statusline
cp <swarmery-repo>/plugins/core/statusline/{statusline.sh,fetch-fable-usage.sh} .claude/statusline/
chmod +x .claude/statusline/*.sh
```

Для ручного шляху також додайте до `.claude/settings.json`:

```jsonc
{ "statusLine": { "type": "command", "command": "bash $CLAUDE_PROJECT_DIR/.claude/statusline/statusline.sh" } }
```

Змінні-перемикачі (усі необов’язкові — виконайте `/statusline-help` у сесії, щоб отримати
повний довідник по кожному полю):

- `SWARMERY_STATUSLINE_LOC` — місто для погоди (порожнє = автоматично за IP).
- `SWARMERY_STATUSLINE_USER=1` — замінити заголовок на email підписки Claude, під якою
  працює сесія. Суто локальне читання
  `$CLAUDE_CONFIG_DIR/.claude.json` (багатоакаунтні конфігурації перемикають підписки
  через цю змінну), інакше `~/.claude.json` — без мережі.
- `SWARMERY_STATUSLINE_FABLE=1` — сегмент тижневого використання Fable-5 за бажанням (OAuth-токен
  із Keychain macOS, кеш на кожен акаунт; TTL задає
  `SWARMERY_STATUSLINE_FABLE_TTL` у секундах, за замовчуванням 300).

Щоб пізніше прибрати statusline, видаліть `.claude/statusline/` і ключ
`statusLine` з налаштувань — ніщо не поверне їх у вас за спиною
(`swarmery offboard --full` також прибирає обидва в межах повного від’єднання).

Ручні кроки нижче описують те, що робить init.sh, — на випадок, коли потрібно щось підлаштувати.

## 1. Створіть конфіг флейвору проєкту
Візьміть за зразок `overlays/example/`:
- `project.json` — конфіг флейвору (схема: `overlays/_schema/project.schema.json`): репозиторії, головний застосунок,
  репозиторій пристрою/edge, хмарні налаштування, доменні терміни, scope комітів, увімкнені пакети.
- `settings.snippet.json` — блок для `.claude/settings.json` (маркетплейс + enabledPlugins + `env.AGENT_PROJECT`).

Тримайте *справжні* заповнені конфіги у своєму проєкті (або в приватному репозиторії робочого простору) — не в цьому публічному репозиторії.

## 2. Прив’яжіть `.claude/` проєкту
Домерджте сніпет у `.claude/settings.json` проєкту:
```jsonc
{
  "extraKnownMarketplaces": { "swarmery": { "source": { "source": "github", "repo": "atretyak1985/swarmery" } } },
  "enabledPlugins": { "core@swarmery": true, "<pack>@swarmery": true },
  "env": { "AGENT_PROJECT": "<project>", "AGENT_WORKSPACE_ROOT": "/path/to/swarmery-workspace" }
}
```
Розгорніть конфіг флейвору в `<project>/.claude/project.json`.
Агенти проєкту в `.claude/agents/` перекривають агентів плагінів за іменем (нативна база + оверлей).

> [!WARNING]
> **Обережно з переходом:** якщо проєкт раніше працював на файловій копії цієї системи агентів із
> хуками, зареєстрованими в його `settings.json`, приберіть ту успадковану прив’язку хуків у тій самій зміні, що
> вмикає плагіни, — інакше кожен хук спрацьовуватиме двічі. Робіть перемикання у новій сесії.

## 3. Робочий простір
`agent-work.sh` читає `AGENT_PROJECT` + `AGENT_WORKSPACE_ROOT` і пише в
`<workspace-root>/<project>/workspace/…` автоматично. Якщо проєкт новий, додайте його теку в репозиторій робочого простору.

## Контрольний тест (доведіть, що портування мертве)
1. Підніміть `version` у `plugins/core/.claude-plugin/plugin.json`; запуште.
2. У кожному споживачі: `/plugin update`.
3. Переконайтеся, що зміна дійшла до кожного проєкту з **нульовим копіюванням файлів по проєктах**.

Саме заради цього swarmery й існує — перевірте це явно, щойно живих споживачів стане ≥2.

Цей тест ламають дві речі, і обидві ламають тихо:

- **Не піднято версію.** Споживачі беруть пакет за версією, а `marketplace.json` не несе версії
  для кожного плагіна — єдине число, що має значення, це `version` у
  `plugins/<pack>/.claude-plugin/plugin.json`. Запуште без підняття — і `/plugin update` не матиме
  чого завантажувати: кожен споживач лишиться з копією, яку вже має, і ніхто не повідомить про помилку.
- **Тестування на локальному клоні.** Сесія завантажує пакет із `~/.claude/plugins/cache`, ніколи
  з `plugins/**` у клоні — тож правка цього репозиторію й повторний запуск сесії нічого не доводять про
  те, що отримають споживачі. Щоб випробувати роботу, яка ще не закомічена й не випущена, відкрийте
  сесію через `claude --plugin-dir plugins/core` (повторюваний, один прапорець на пакет).

Повний шлях від цього репозиторію до робочої сесії — кеш, `--plugin-dir` і що робити, коли
`enabledPlugins` і кеш розходяться, — описаний у [PLUGINS.md](PLUGINS.md#how-a-plugin-reaches-your-session),
у розділі «How a plugin reaches your session».
