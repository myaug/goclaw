# Bộ Agent: Binance Square Crypto Analyst

Pack này là bộ `.md` hoàn chỉnh cho một `predefined agent` trong GoClaw, chuyên biến:

- link post Telegram thành bài phân tích mới, có kiểm chứng
- yêu cầu cụ thể về BTCUSDT, ETHUSDT, BNBUSDT, SOLUSDT, altcoins thành bài viết đăng Binance Square
- trường hợp không có input thành workflow tự tìm chủ đề hot nhất từ các nguồn crypto uy tín

Mục tiêu của pack này là tạo ra agent viết bài theo phong cách Binance Square nhưng không giả danh Binance, không đưa ra financial advice, không sao chép nội dung bên ngoài, và không bịa dữ liệu.

## Ánh Xạ Runtime

Agent-level files cho `predefined agent`:

- `AGENTS.md`
- `AGENTS_TASK.md`
- `AGENTS_CORE.md`
- `SOUL.md`
- `CAPABILITIES.md`
- `IDENTITY.md`
- `USER_PREDEFINED.md`
- `TOOLS.md`

Per-user files:

- `USER.md`
- `BOOTSTRAP.md`

## Agent Này Làm Gì

- Viết bài phân tích crypto để đăng Binance Square theo dạng `Post` mặc định.
- Nếu bài cần chiều sâu hơn, có thể nâng thành `Article` khi user yêu cầu rõ hoặc chủ đề quá lớn.
- Nếu được đưa 1 link Telegram, agent dùng nó như một lead để xác minh và viết lại từ đầu, không sao chép.
- Nếu được giao 1 coin cụ thể, agent sẽ ưu tiên phân tích dữ liệu `1w`, `1d`, `4h` từ Binance Spot `klines` hoặc `uiKlines` để đọc market structure, candlesticks, và các kịch bản xác suất.
- Nếu không có input, agent tự tìm chủ đề hot theo thứ tự nguồn đã định sẵn.

## Baseline Compliance

Pack này đã mã hóa các rule sau vào prompt files:

- Nội dung là `analysis` và `market commentary`, không phải financial advice.
- Không đưa lệnh giao dịch trực tiếp kiểu `buy now`, `sell now`, `ape in`, `all-in`.
- Không cam kết lợi nhuận, không bịa PnL, không đưa false performance claims.
- Không shill, không spam, không pump, không referral links, không QR code, không funnel sang paid group.
- Không copy bài Telegram, bài báo, hoặc bài của creator khác.
- Không đưa thông tin chưa kiểm chứng như một sự thật.
- Không tạo ấn tượng agent là kênh chính thức của Binance.

## Nguồn Dữ Liệu Và Tin Tức

Topic discovery ưu tiên:

1. Binance Square News
2. Binance Announcements
3. Binance Research
4. CoinDesk
5. The Block
6. Messari
7. Cointelegraph để bắt lead nhanh
8. Official blog/X đã verify của dự án liên quan

Market data ưu tiên:

- `https://api.binance.com/api/v3/klines`
- `https://api.binance.com/api/v3/uiKlines`
- `https://api.binance.com/api/v3/exchangeInfo`
- `https://data-api.binance.vision` khi cần market-data-only host

## Cách Nạp Đề Xuất

1. Tạo một `predefined agent` trong GoClaw.
2. Nạp các file agent-level trong folder này vào `agent_context_files` của agent đó.
3. Dùng `USER.md` và `BOOTSTRAP.md` trong folder này làm template per-user.
4. Nếu anh có watchlist riêng, ngôn ngữ xuất bản, hoặc tone riêng, sửa `USER_PREDEFINED.md` trước khi sử dụng.

## Ghi Chú

- Mặc định pack này tối ưu cho owner chat bằng tiếng Việt, nhưng bài đăng Binance Square sẽ theo language mà user yêu cầu; nếu user không chỉ định, draft mặc định là tiếng Anh để phù hợp audience rộng hơn.
- Nếu anh muốn agent viết thuần tiếng Việt cho audience Việt Nam, chỉ cần sửa `USER_PREDEFINED.md` và `SOUL.md`.
- Nếu anh muốn bộ này dùng cho dashboard/wizard, hãy giữ nguyên runtime filenames như trong folder.