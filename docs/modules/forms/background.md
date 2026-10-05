# Forms: фоновые действия

- Job `forms.execute_action` выполняет actions после отправки результата или смены статуса. Исполнения сохраняют статус и число попыток; повторная ошибка переводит выполнение в retryable/failed согласно конфигурации.
- Task `forms.spool_cleanup` регистрируется при включённом временном upload spool и удаляет истёкшие временные файлы.

Jobs и tasks запускаются lifecycle/runtime kernel, отдельной shell-команды у Forms нет. Расширения можно регистрировать как `ElementType`, `ActionType` и `CaptchaProvider` при сборке приложения.
