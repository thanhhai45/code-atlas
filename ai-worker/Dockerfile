FROM python:3.11-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY scripts ./scripts
# Bake the model into the image so containers start without network access.
RUN python scripts/fetch_model.py /models/all-MiniLM-L6-v2
COPY app.py .
ENV MODEL_DIR=/models/all-MiniLM-L6-v2
RUN useradd -u 10001 worker
USER worker
EXPOSE 8000
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
