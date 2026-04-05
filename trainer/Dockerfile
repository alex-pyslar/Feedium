FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Скачиваем bge-m3 при сборке (570MB, избегаем задержки на старте)
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-m3')"

COPY . .

# По умолчанию: однократный прогон.
# Для планового обучения каждые 12ч:
#   command: ["python", "main.py", "--schedule", "--interval", "12"]
CMD ["python", "main.py"]
