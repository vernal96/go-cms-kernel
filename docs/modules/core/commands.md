# Core: консольные команды

Core регистрирует в kernel console команды `users`, `groups` и `permissions`. Они требуют запущенного (booted) приложения, работают с системным актором и печатают результат в JSON. Их используют для администрирования и автоматизации; это не HTTP API.

Во всех командах флаги пишутся как `--name value` или `--name=value`. Неизвестные флаги и лишние позиционные аргументы приводят к ошибке. Идентификаторы пользователей и групп должны быть положительными целыми числами.

Краткая подсказка доступна через `console <command> help`, например `console users help`. Ниже перечислены текущие подкоманды и их флаги.

## `users` — учётные записи

### `users create`

Создаёт пользователя и сразу включает его в одну или несколько групп.

| Флаг | Обязательный | Описание |
| --- | --- | --- |
| `--login` | Да | Уникальный логин |
| `--email` | Да | Уникальный email |
| `--name` | Да | Имя |
| `--last-name` | Нет | Фамилия |
| `--middle-name` | Нет | Отчество |
| `--phone` | Нет | Телефон |
| `--avatar-media-id` | Нет | Положительный ID существующего media |
| `--group` | Да, минимум один | Код группы; повторяйте флаг для нескольких групп |
| `--generate-password` | Нет | Создать случайный пароль и включить его в JSON-ответ |

Без `--generate-password` команда читает пароль из stdin одной строкой. Пустой пароль отклоняется. Коды групп сравниваются без учёта регистра; пустые, неизвестные и повторяющиеся коды отклоняются.

```sh
console users create \
  --login editor \
  --email editor@example.test \
  --name 'Иван Редактор' \
  --group editor
```

В этом примере пароль будет прочитан из stdin. Сгенерированный вариант:

```sh
console users create \
  --login editor \
  --email editor@example.test \
  --name 'Иван Редактор' \
  --group editor \
  --generate-password
```

Ответ содержит `user`, выбранные `groups` и `generated_password` только при генерации. Сохраните выданный пароль: он показывается этим ответом.

### `users get` и `users list`

- `console users get --id 17` — возвращает одну запись пользователя; `--id` обязателен.
- `console users list` — возвращает список пользователей; дополнительные аргументы не принимаются.

```sh
console users get --id 17
console users list
```

### `users update`

Меняет только те поля, флаги которых переданы. Неуказанные поля сохраняются.

| Флаг | Описание |
| --- | --- |
| `--id` | Обязательный положительный ID пользователя |
| `--login` | Новый логин |
| `--email` | Новый email |
| `--name` | Новое имя |
| `--last-name` | Фамилия; пустое значение очищает её |
| `--middle-name` | Отчество; пустое значение очищает его |
| `--phone` | Телефон; пустое значение очищает его |
| `--avatar-media-id` | ID media; пустое значение удаляет аватар |

```sh
console users update --id 17 --email editor@example.test --phone '+7 900 123-45-67'
console users update --id 17 --last-name '' --avatar-media-id ''
```

### `users password`

Сменяет пароль выбранного пользователя. Требует `--id` и читает новую непустую строку из stdin; пароль не передаётся аргументом командной строки.

```sh
console users password --id 17 < /secure/path/new-password.txt
```

Команда читает одну строку; завершающий LF/CRLF удаляется.

### `users block` и `users unblock`

Блокирует пользователя или снимает блокировку. Обе команды требуют `--id` и возвращают обновлённую запись.

```sh
console users block --id 17
console users unblock --id 17
```

Нельзя заблокировать самого себя; также сервис защищает последнего активного администратора.

## `groups` — группы, участники и разрешения

### `groups create`

Создаёт группу.

| Флаг | Описание |
| --- | --- |
| `--code` | Обязательный код группы |
| `--name` | Обязательное название группы |
| `--super` | Признак группы с super-привилегиями; по умолчанию `false` |

```sh
console groups create --code editor --name 'Редакторы'
```

Установка `--super` требует привилегированного доступа.

### `groups get`, `groups list`, `groups delete`

- `console groups get --id 4` — получить группу по ID.
- `console groups list` — перечислить группы; флаги и аргументы не принимаются.
- `console groups delete --id 4` — удалить группу; ответ содержит `deleted` и `group_id`.

```sh
console groups get --id 4
console groups list
console groups delete --id 4
```

`get` и `delete` требуют `--id`. Защищённые группы и удаление последнего администратора могут быть запрещены сервисом.

### `groups update`

Требует `--id`; изменяет только переданные поля, сохраняя остальные текущие значения.

| Флаг | Описание |
| --- | --- |
| `--id` | Положительный ID группы |
| `--name` | Новое название |
| `--super` | Установить `true` или `false` |

```sh
console groups update --id 4 --name 'Старшие редакторы'
console groups update --id 4 --super=false
```

### `groups members` и `groups permissions`

Обе команды принимают обязательный `--id` группы:

- `console groups members --id 4` — список пользователей в группе.
- `console groups permissions --id 4` — список разрешений группы.

```sh
console groups members --id 4
console groups permissions --id 4
```

### `groups add-user` и `groups remove-user`

Изменяют членство. Обе команды требуют положительные `--group` (ID группы) и `--user` (ID пользователя).

```sh
console groups add-user --group 4 --user 17
console groups remove-user --group 4 --user 17
```

Добавление возвращает запись членства; удаление — подтверждение с ID группы и пользователя.

### `groups grant` и `groups revoke`

Назначают группе разрешение либо отзывают его. Флаги: `--group` (положительный ID группы) и `--permission` (непустой код разрешения).

```sh
console groups grant --group 4 --permission core.resource.read
console groups revoke --group 4 --permission core.resource.read
```

Код разрешения должен существовать в каталоге. Сначала проверьте доступные коды командой `console permissions list`.

## `permissions` — каталог и доступ гостей

### `permissions list`

Выводит зарегистрированные в приложении коды разрешений. Не принимает флагов или аргументов.

```sh
console permissions list
```

### `permissions guest-list`

Выводит разрешения, назначенные гостевому актору. Не принимает флагов или аргументов.

```sh
console permissions guest-list
```

### `permissions guest-grant` и `permissions guest-revoke`

Выдают гостям разрешение или отзывают его. Требуют `--permission` с непустым кодом из каталога.

```sh
console permissions guest-grant --permission core.resource.read
console permissions guest-revoke --permission core.resource.read
```

Выдавайте гостям только те разрешения, которые должны быть доступны без пользовательской сессии. Отзыв возвращает подтверждение операции.
