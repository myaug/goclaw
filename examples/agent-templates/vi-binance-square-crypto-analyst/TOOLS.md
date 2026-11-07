# TOOLS.md - Ghi Chú Nghiên Cứu Và Dữ Liệu

## Nguồn Chủ Đề Ưu Tiên

Ưu tiên quét lead theo thứ tự này:

1. CryptoQuant
2. Glassnode
3. CryptoRank
4. CoinDesk
5. The Block
6. Messari
7. Cointelegraph để bắt lead nhanh
8. Official blog / governance forum / X đã verify của dự án liên quan

## Endpoint Dữ Liệu Thị Trường Binance

Ưu tiên dùng Spot REST docs-supported endpoints.

- Validate symbol:
  - `https://api.binance.com/api/v3/exchangeInfo?symbol=BTCUSDT`
- Standard klines:
  - `https://api.binance.com/api/v3/klines?symbol=BTCUSDT&interval=1w&limit=200`
  - `https://api.binance.com/api/v3/klines?symbol=ETHUSDT&interval=1d&limit=200`
  - `https://api.binance.com/api/v3/klines?symbol=SOLUSDT&interval=4h&limit=300`
- UI-oriented bars:
  - `https://api.binance.com/api/v3/uiKlines?symbol=BTCUSDT&interval=1w&limit=200`
- Market-data-only host:
  - `https://data-api.binance.vision/api/v3/klines?symbol=BTCUSDT&interval=1d&limit=200`

Không ưu tiên scrape UI/XHR không có docs.

Không ưu tiên Alpha Klines cho workflow mặc định vì symbol format khác spot thông thường.

## Heuristic Chấm Điểm Chủ Đề

Khi có nhiều lead, chấm điểm theo:

- recency
- market impact
- cross-source confirmation
- price reaction
- fit với audience Binance Square

Chọn lead có signal rõ nhất, không nhất thiết lead ồn ào nhất.

## Cách Xử Lý Đầu Vào Từ Telegram

- Dùng Telegram link như lead, không dùng như bản sao.
- Rút claim, symbol, timeline, catalyst.
- Kiểm chứng trước khi viết.
- Nếu bài gốc giống kiểu shill, tách ra chỉ lấy data có thể xác minh.
- Không paraphrase sát mức có nguy cơ plagiarism.

## Ghi Chú Khi Viết Cho Square

- `Post` giới hạn 2100 ký tự, nên ưu tiên mặc định.
- `Article` chỉ dùng khi owner muốn depth.
- Cashtag, hashtag, token mention, sentiment tag có thể gợi ý ở notes ngoài bài đăng.
- Trong bài đăng, ưu tiên readability trên mobile: line ngắn, thesis sớm, close gọn.

## Những Thứ Phải Tránh

- referral link
- QR code
- paid room CTA
- fake target
- fake performance
- unverifiable rumor frame thành fact
- implied Binance endorsement