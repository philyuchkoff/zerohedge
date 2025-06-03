#!/bin/bash

# ZeroHedge Monitor - запуск с переменными окружения
# Использование: ./run.sh [start|stop|restart|status]

set -euo pipefail

# --- Конфигурация ---
APP_NAME="zerohedge-monitor"
APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_FILE="${APP_DIR}/zerohedge_monitor.log"
PID_FILE="${APP_DIR}/${APP_NAME}.pid"
GO_CMD="go run main.go"  # Или путь к бинарнику

# Проверка переменных окружения
check_env() {
  local missing=()
  [[ -z "${TG_TOKEN:-}" ]] && missing+=("TG_TOKEN")
  [[ -z "${TG_CHAT_ID:-}" ]] && missing+=("TG_CHAT_ID")

  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "Ошибка: Не заданы обязательные переменные окружения:"
    printf ' - %s\n' "${missing[@]}"
    echo "Задайте их в файле .env или экспортируйте вручную"
    exit 1
  fi
}

# Загрузка .env файла
load_env() {
  if [[ -f "${APP_DIR}/.env" ]]; then
    set -o allexport
    source "${APP_DIR}/.env"
    set +o allexport
  fi
}

# Запуск приложения
start() {
  check_env
  echo "Запуск ${APP_NAME}..."
  
  nohup ${GO_CMD} >> "${LOG_FILE}" 2>&1 &
  echo $! > "${PID_FILE}"

  echo "Приложение запущено (PID: $(cat "${PID_FILE}"))"
  echo "Логи: tail -f ${LOG_FILE}"
}

# Остановка приложения
stop() {
  if [[ -f "${PID_FILE}" ]]; then
    local pid=$(cat "${PID_FILE}")
    if kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}"
      echo "Приложение остановлено (PID: ${pid})"
    else
      echo "Процесс ${pid} не найден"
    fi
    rm -f "${PID_FILE}"
  else
    echo "Файл PID не найден - приложение, вероятно, не запущено"
  fi
}

# Статус приложения
status() {
  if [[ -f "${PID_FILE}" ]]; then
    local pid=$(cat "${PID_FILE}")
    if kill -0 "${pid}" 2>/dev/null; then
      echo "Приложение работает (PID: ${pid})"
    else
      echo "Приложение не работает (устаревший PID: ${pid})"
    fi
  else
    echo "Приложение не запущено"
  fi
}

# Основная логика
case "${1:-}" in
  start)
    load_env
    start
    ;;
  stop)
    stop
    ;;
  restart)
    stop
    sleep 2
    load_env
    start
    ;;
  status)
    status
    ;;
  *)
    echo "Использование: $0 {start|stop|restart|status}"
    exit 1
    ;;
esac
