# IDA Pro 9.3 SP1 — ручная установка на Linux

EnvForge для IDA делает только две вещи:

1. ставит системные зависимости, нужные Qt/XCB;
2. проверяет, появилась ли установленная IDA по пути `~/ida-pro-9.3/ida`.

Сам архив, запуск инсталлятора и сама GUI-установка делаются пользователем вручную.

---

## 1. Подготовить файлы

Держите архив и инсталлер локально, не в public GitHub Release.

Если имя файла повреждено markdown-ссылкой, переименуйте его в нормальный вид. Пример:

```bash
mv 'ida-pro_93_[x64linux.run](http://x64linux.run)' ida-pro_93_x64linux.run
chmod +x ida-pro_93_x64linux.run
```

---

## 2. Установить зависимости

Это можно сделать через EnvForge, выбрав пакет IDA, либо вручную:

```bash
sudo apt update
sudo apt install -y \
  libxcb-cursor0 \
  libxcb-icccm4 \
  libxcb-image0 \
  libxcb-keysyms1 \
  libxcb-randr0 \
  libxcb-render-util0 \
  libxcb-shape0 \
  libxcb-xfixes0 \
  libxcb-xkb1 \
  libxkbcommon-x11-0 \
  libx11-6 \
  libx11-xcb1 \
  libxext6 \
  libxrender1 \
  libxi6 \
  libxrandr2 \
  libxcb1 \
  libxcb-glx0 \
  libxcb-shm0 \
  libxkbcommon0 \
  libfontconfig1 \
  libfreetype6 \
  libglib2.0-0 \
  libdbus-1-3 \
  libnss3 \
  libegl1 \
  libgl1

sudo apt install -y libasound2t64 || sudo apt install -y libasound2
```

---

## 3. Запустить GUI-инсталлер

Запускать именно как исполняемый файл, а не через `sh`:

```bash
./ida-pro_93_x64linux.run
```

Если используется Wayland и окно не появляется, можно попробовать:

```bash
QT_QPA_PLATFORM=xcb ./ida-pro_93_x64linux.run
```

---

## 4. Куда ставить

Рекомендуемый путь установки:

```bash
/home/<user>/ida-pro-9.3
```

То есть для обычного пользователя это:

```bash
~/ida-pro-9.3
```

---

## 5. Проверка после установки

Проверить, что бинарь появился:

```bash
ls -la ~/ida-pro-9.3
```

Проверить запуск:

```bash
~/ida-pro-9.3/ida
```

Проверить через EnvForge:

```bash
./envforge --check
```

EnvForge считает Linux-установку IDA успешной, если существует исполняемый файл:

```bash
~/ida-pro-9.3/ida
```

---

## 6. Что не делает EnvForge

EnvForge не делает:

- загрузку пользовательского архива;
- запуск `keygen`;
- модификацию лицензии;
- патч библиотек;
- публикацию архива.

Эти шаги остаются полностью ручными.
