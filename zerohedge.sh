#!/bin/bash

# Загрузка переменных окружения
if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
else
    echo ".env файл не найден"
    exit 1
fi

# Проверка обязательных переменных
required_vars=("TG_TOKEN" "TG_CHAT_ID" "YANDEX_TRANSLATE_KEY" "YANDEX_FOLDER_ID")
missing_vars=()

for var in "${required_vars[@]}"; do
    if [ -z "${!var}" ]; then
        missing_vars+=("$var")
    fi
done

if [ ${#missing_vars[@]} -ne 0 ]; then
    echo "Отсутствуют обязательные переменные окружения:"
    printf '%s\n' "${missing_vars[@]}"
    exit 1
fi

# Создание директории для логов если её нет
mkdir -p logs

# Запуск монитора
echo "Запуск ZeroHedge монитора..."
./zerohedge_monitor 2>&1 | tee -a logs/zerohedge_monitor.log
