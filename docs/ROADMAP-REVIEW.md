# Đánh giá Roadmap — OpenSource Intelligence Search Engine

> Review của [`ROADMAP.md`](ROADMAP.md) trước khi triển khai. Kết luận: **roadmap tốt, đáng làm**,
> nhưng cần chỉnh một số giả định về dữ liệu, timeline và cách đo chất lượng search.

## 1. Kết luận nhanh

| Tiêu chí | Đánh giá |
|---|---|
| Tầm nhìn sản phẩm | ✅ Rõ ràng — "discovery layer", không clone GitHub |
| Lựa chọn Elasticsearch làm lõi | ✅ Hợp lý — bài toán cần đúng những thứ ES mạnh (BM25, facet, fuzzy, vector, hybrid) |
| Tech stack | ✅ Hợp lý, hơi nhiều thành phần cho giai đoạn đầu |
| Thứ tự phase (MVP → quality → scale → AI → cluster) | ✅ Rất đúng |
| Chiến lược tài liệu hoá thí nghiệm | ✅ Điểm mạnh nhất của roadmap |
| Timeline 12 tuần | ⚠️ Lạc quan, nhất là tuần 9–12 |
| Nguồn dữ liệu cho 10M–50M documents | ⚠️ GitHub API không đủ, cần nguồn khác |
| Đo chất lượng ranking | ⚠️ Thiếu phương pháp (judgment list / NDCG) |
| Observability | ⚠️ Đặt quá muộn (Phase 14) |

**Quyết định:** triển khai ngay Phase 0 + Phase 1, và một phần Phase 4/5/6/7 (query DSL, function_score,
autocomplete, fuzzy, synonyms, highlight, facets) vì các phần này rẻ khi làm cùng lúc với mapping đầu tiên.

## 2. Điểm mạnh

1. **Phân tầng dữ liệu đúng:** PostgreSQL là source of truth, Elasticsearch là index dẫn xuất có thể rebuild.
   Đây là nền tảng để reindex an toàn về sau.
2. **"Không bắt đầu với 50M documents"** và bảng milestone M1→M5 rất thực tế.
3. **AI chỉ enrich lúc index, không nằm trên đường request** — đúng về latency và chi phí.
4. **Mọi thí nghiệm theo khung Hypothesis → Result → Production implication** — biến project học thành portfolio có giá trị.
5. **Phần Failure & Incident Lab** hiếm roadmap nào có, rất sát công việc production thực tế.

## 3. Rủi ro và đề xuất chỉnh sửa

### 3.1 Giới hạn GitHub API (quan trọng nhất)

- Search API trả **tối đa 1000 kết quả mỗi query** (10 trang × 100) và chỉ **30 request/phút** (có token; 10 nếu không).
  Muốn lấy 100K repo phải **chia nhỏ query** — code hiện tại dùng "star cursor": sort theo stars giảm dần,
  hết 1000 kết quả thì query tiếp `stars:<=min_đã_thấy`, loại trùng theo id. Khi 1000 repo có cùng số star
  (vùng star thấp) cần chia tiếp theo `created:` — đã ghi TODO.
- Core API **5000 request/giờ**: lấy README cho 100K repo ≈ 20 giờ với 1 token. Cần incremental sync (chỉ
  fetch lại repo có `pushed_at` mới) — bảng `crawl_runs` đã được tạo làm nền.
- **50M commits qua REST API là không khả thi.** Với M4/M5 nên dùng:
  [GH Archive](https://www.gharchive.org/) (event stream theo giờ), GitHub public dataset trên BigQuery,
  [ecosyste.ms](https://ecosyste.ms/) (packages + dependencies), hoặc Software Heritage.
  Ngoài ra có thể dùng GraphQL API để giảm số request khi cần nhiều field/repo.

### 3.2 Timeline

Tuần 1–8 khả thi cho 1 người làm part-time nghiêm túc. Tuần 9–12 (AI + vector + 50M docs + cluster + AWS)
thực tế là **8–12 tuần**. Đề xuất: coi "12 tuần" là lộ trình **MVP + search quality**, phần scale/production
là một roadmap thứ hai.

### 3.3 Đo chất lượng search

Roadmap nói "benchmark và tune ranking" nhưng chưa có ground truth. Đề xuất bổ sung ở Phase 5:

- Tạo **judgment list** ~50–100 query (vd: "go vector database" → milvus, weaviate là relevant).
- Dùng **Ranking Evaluation API (`_rank_eval`)** của Elasticsearch để tính **NDCG@10 / MRR / Precision@k**.
- Mỗi thay đổi boost / function_score phải chạy lại và ghi kết quả vào `docs/experiments/`.

### 3.4 Observability nên có từ đầu

"Search latency is measured" nằm trong MVP DoD nhưng Prometheus để ở Phase 14. Đã xử lý: API expose
`/metrics` (histogram latency theo route, `took` của Elasticsearch, cache hit/miss) ngay từ Phase 0.
Grafana dashboard có thể thêm sau.

### 3.5 Thiết kế index cho nhiều entity

Khi thêm Release/Commit/Issue: dùng **index riêng cho mỗi entity** (`releases`, `commits`, …), tránh
`nested`/`join` cho quan hệ 1-nhiều lớn (commit). `nested` chỉ nên dùng cho mảng nhỏ bị giới hạn
(vd: dependencies có version). Commit/issue nên dùng time-based index + ILM.

### 3.6 Reindex strategy từ ngày đầu

Roadmap đặt versioned index/alias ở Phase 13. Đã làm ngay: mọi đọc/ghi đi qua alias `repositories`,
index thật là `repositories_<timestamp>`, và `crawler -reindex` build index mới từ PostgreSQL rồi
swap alias atomically (đã test thực tế khi đổi mapping).

### 3.7 Các lưu ý nhỏ

- **License Elasticsearch:** ES là AGPLv3 / SSPL / Elastic License 2.0 — self-host thoải mái. Một số tính năng
  (vd. một số dạng hybrid ranking/RRF, ML inference) phụ thuộc phiên bản và license tier — kiểm tra trước khi
  thiết kế Phase 9; có thể tự fuse BM25 + kNN ở tầng ứng dụng. OpenSearch là phương án thay thế Apache-2.0.
- **Redis làm queue:** đủ cho giai đoạn đầu; cân nhắc queue trên PostgreSQL (`FOR UPDATE SKIP LOCKED`) để
  bớt một thành phần và có transaction cùng dữ liệu.
- **README rất dài** làm phình index và lệch BM25 length normalization → đã cắt ở 20KB.
- **Chi phí AWS** cho 3 node ES trên EC2 đáng kể; phần cluster/failure lab nên làm bằng Docker Compose local
  trước, AWS chỉ để deploy cuối.
- **Security:** local tắt xpack security cho tiện; production bắt buộc bật TLS + auth, không expose 9200.

## 4. Phạm vi đã triển khai trong lần này

| Phase | Nội dung | Trạng thái |
|---|---|---|
| 0 | Go + Gin API, Next.js, PostgreSQL, Redis, Elasticsearch, Docker Compose, CI, health check | ✅ |
| 1 | Crawler GitHub: pagination, rate limit (primary + secondary), retry/backoff, idempotent upsert, bulk index, star cursor vượt giới hạn 1000 | ✅ |
| 3 | Mapping `dynamic: strict`, custom analyzers (word_delimiter cho tên repo, synonyms lúc search), keyword normalizer | ✅ |
| 4 | Bool query must/filter, terms/range filters | ✅ |
| 5 | Field boosts + function_score; judgment list 40 query + `cmd/rankeval` (`_rank_eval`: NDCG, MRR, precision, recall), cổng chặn regression trong CI; trọng số business signals đã tune bằng `rankeval -grid` (sum: 0.5·log stars + 1.0·recency) — xem `docs/experiments/02` và `03` | ✅ trên dữ liệu mẫu (cần tune lại với dữ liệu thật) |
| 6 | Autocomplete (search_as_you_type), fuzzy, synonyms, highlight; from/size trong giới hạn 10K + `search_after` với cursor cho phân trang sâu (integration test trên ES thật trong CI) | ✅ |
| 7 | Facets multi-select đúng chuẩn (post_filter + filter agg), stars range, activity date_range | ✅ |
| 9 | Similar repositories bằng `more_like_this` (baseline lexical để so với vector sau này) | 🟡 baseline |
| 13 | Versioned index + alias swap reindex | ✅ (sớm) |
| 14 | Prometheus metrics trong API | 🟡 (chưa có Grafana) |

Việc tiếp theo đề xuất: (1) crawl 10K repo thật với token rồi mở rộng judgment list, (2) tune lại trọng số
business signals trên dữ liệu thật, (3) viết kết quả cho `docs/experiments/01-fundamentals.md`, (4) chia query theo
`created:` khi star cursor bị kẹt.
