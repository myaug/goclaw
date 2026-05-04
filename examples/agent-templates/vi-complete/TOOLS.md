# TOOLS.md - Local Notes

_Skill giải thích tool hoạt động như thế nào. File này là nơi giữ các chi tiết chỉ đúng với setup của riêng bạn._

## What Goes Here

Những thứ kiểu như:

- Tên camera và vị trí của chúng
- SSH host, alias, bastion, jump host
- Voice TTS ưa dùng
- Tên loa, phòng, thiết bị
- Nickname của máy, server, bot, hoặc endpoint
- Bất kỳ chi tiết môi trường nào giúp bạn thao tác nhanh và đúng hơn

## Examples

```markdown
### Cameras

- living-room → Khu sinh hoạt chính, góc rộng 180 độ
- front-door → Cửa trước, có motion trigger

### SSH

- home-server → 192.168.1.100, user: admin

### TTS

- Preferred voice: "Nova" (ấm, hơi hướng Anh)
- Default speaker: Kitchen HomePod
```

## Media Files

Khi người dùng gửi ảnh, video, audio, hoặc tài liệu, hãy dùng đúng `read_*` tool tương ứng để phân tích.

Thuộc tính `path` trong media tag chính là đường dẫn cần truyền vào tool.

## Why Separate?

Skill là thứ có thể chia sẻ. Còn setup của bạn là thứ mang tính cục bộ.

Giữ tách riêng giúp bạn:

- cập nhật skill mà không làm mất local notes
- chia sẻ skill mà không vô tình lộ hạ tầng riêng
- thay đổi môi trường nhanh mà không đụng vào persona hay operating rules

---

Hãy coi file này như cheat sheet cho môi trường sống của bạn.