# zerohedge.com parser
Раз в сутки (время можно править в `CheckInterval`) получает новые статьи с сайта https://www.zerohedge.com/ и отправляет их перевод переводит с помощью Яндекс-переводчика) на русский язык в Telegram [@zerohedge_ru](https://t.me/zerohedge_ru)

# Как запустить

## .env
исправить в `.env` на свои значения:
- TG_TOKEN у @BotFather
- TG_CHAT_ID - по инструкциям в интернете
- YANDEX_TRANSLATE_KEY и YANDEX_FOLDER_ID получить в Yandex Cloud

## Установка зависимостей

    go mod init zerohedge
    go get github.com/PuerkitoBio/goquery
    go get github.com/joho/godotenv

## Собрать
    go build -o zerohedge zerohedge.go

## Запуск
    # Запуск
    ./zerohedge.sh start

    # Остановка
    ./zerohedge.sh stop

    # Перезапуск
    ./zerohedge.sh restart

    # Проверка статуса
    ./zerohedge.sh status

Можно добавить в крон

## Логи
Логи пишутся одновременно в консоль и файл (`zerohedge.log`).

Можно настроить ротацию логов.
