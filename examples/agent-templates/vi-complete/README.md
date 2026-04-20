# Bộ Template Agent Tiếng Việt

Bộ này là pack tham chiếu tiếng Việt cho toàn bộ stack file agent hiện hành của repo GoClaw.

Mục tiêu:

- Giữ đúng tên file và ranh giới trách nhiệm giữa từng file như runtime hiện tại.
- Giữ các heading hoặc field quan trọng bằng tiếng Anh ở những nơi cần tương thích với convention của repo.
- Dùng tiếng Việt cho phần hướng dẫn, mô tả, placeholder, và nội dung mẫu để dễ authoring.

Lưu ý quan trọng:

- Embedded templates runtime tại `internal/bootstrap/templates/` vẫn nên giữ tiếng Anh theo convention của repo.
- Pack này dành cho tham chiếu, authoring, onboarding, hoặc làm bộ seed tùy biến; không ghi đè trực tiếp lên template nhúng mặc định trừ khi bạn chủ động đổi chiến lược.

## File trong pack này

- `AGENTS.md` — luật vận hành chính cho full mode.
- `AGENTS_TASK.md` — luật vận hành rút gọn cho task mode.
- `AGENTS_CORE.md` — luật tối thiểu cho minimal mode.
- `SOUL.md` — persona, giọng điệu, ranh giới, phong cách.
- `CAPABILITIES.md` — năng lực chuyên môn, workflow, phương pháp.
- `IDENTITY.md` — name, creature, purpose, vibe, emoji, avatar.
- `USER.md` — hồ sơ từng người dùng.
- `USER_PREDEFINED.md` — baseline áp dụng cho mọi user của predefined agent.
- `BOOTSTRAP.md` — first-run cho open agent.
- `BOOTSTRAP_PREDEFINED.md` — first-run cho predefined agent.
- `TOOLS.md` — ghi chú local setup, không phải persona.
- `legacy/AGENTS_V1.md` — bản v1 để tham chiếu lịch sử, không phải stack khuyến nghị hiện tại.

## Cách dùng theo agent type

### Open agent

Thường dùng bộ sau theo từng user:

- `AGENTS.md`
- `SOUL.md`
- `CAPABILITIES.md`
- `IDENTITY.md`
- `USER.md`
- `BOOTSTRAP.md`
- `TOOLS.md` nếu có local notes riêng

### Predefined agent

Shared ở agent level:

- `AGENTS.md`
- `AGENTS_TASK.md`
- `AGENTS_CORE.md`
- `SOUL.md`
- `CAPABILITIES.md`
- `IDENTITY.md`
- `USER_PREDEFINED.md`

Per-user:

- `USER.md`
- `BOOTSTRAP_PREDEFINED.md`

## Tương thích với prompt mode hiện tại

- `full` → dùng luật chính ở `AGENTS.md` cùng persona, identity, capabilities và context đầy đủ.
- `task` → dùng `AGENTS_TASK.md` để tập trung vào execution discipline.
- `minimal` → dùng `AGENTS_CORE.md` + `CAPABILITIES.md`.
- `none` → gần như không có operating rules, chỉ phù hợp cho flow rất đặc thù.

## Nguyên tắc authoring

- Persona nằm ở `SOUL.md`, không nhét chuyên môn vào đó.
- Chuyên môn nằm ở `CAPABILITIES.md`, không nhét tính cách vào đó.
- Facts nhận diện nằm ở `IDENTITY.md`.
- Baseline cho mọi người dùng nằm ở `USER_PREDEFINED.md`.
- Hồ sơ từng người dùng nằm ở `USER.md`.
- Luật vận hành, memory discipline, group-chat behavior nằm ở `AGENTS*.md`.
- Bootstrap chỉ lo first-run choreography, không phải nơi nhét persona dài hạn.

## Gợi ý workflow

1. Viết hoặc generate `SOUL.md` + `CAPABILITIES.md` trước.
2. Sau đó chốt `IDENTITY.md`.
3. Nếu là predefined agent, thêm `USER_PREDEFINED.md`.
4. Chọn `AGENTS.md`, `AGENTS_TASK.md`, `AGENTS_CORE.md` theo mode thực tế bạn sẽ chạy.
5. Để `USER.md` và bootstrap file cho runtime điền hoặc tinh chỉnh trong phiên đầu tiên.