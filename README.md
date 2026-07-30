# hunyimg

exe.dev 호환 커스텀 이미지를 만들고, **암호 인증 후 일정 시간만 유효한 이미지 경로**를 발급하는 시스템.

## 구성

```
브라우저 ──HTTPS──▶ exe.dev proxy ──▶ :8000 vend (Go)
                                       ├── /                발급 페이지 (자격증명 경고 표시)
                                       ├── /admin/          관리 페이지 (웹 터미널 + bake)
                                       ├── /api/grant        비밀번호 → 임시 토큰 경로 발급
                                       └── /v2/t/<token>/…   토큰 스코프 registry 프록시
                                                  │
                                                  ▼
                                          :5000 registry:2 (localhost only)
```

- `hunydev/base` — node/npm/npx, python, uv/uvx, go, gh, codex, claude, gemini + systemd/exeuntu 호환 레이어
- `hunydev/dev` — base + 로그인된 자격증명을 구운 이미지
- 발급 시 `LABEL`만 다른 얇은 레이어를 새 태그로 push → 태그별 digest가 고유하므로 만료 시 개별 삭제 가능
- 만료되면 태그·토큰 경로 삭제, blob은 매일 `hunyimg-gc.timer`가 회수

## 웹에서 전체 관리 (`/admin/`)

메인과 **동일한 암호**로 로그인합니다. SSH 없이 브라우저만으로 끝낼 수 있습니다.

1. **자격증명 상태표** — 서비스별 만료 시각을 계산해 남은 시간/경과 시간으로 보여줍니다.
   - `정상` — 유효
   - `액세스 토큰 만료` (노랑) — refresh 토큰이 있어 CLI가 첫 실행 시 자동 갱신. 보통 조치 불필요
   - `만료됨` / `확인 불가` (빨강) — 재로그인 필요
   - `로그인 안됨` (회색) — 아직 OAuth 미완료
2. **웹 터미널** — 베이스 이미지 안에서 도는 실제 PTY(xterm.js ↔ websocket ↔ `docker run -it`).
   표의 행이나 `gh`/`codex`/`claude`/`gemini` 버튼을 누르면 해당 로그인 명령이 바로 입력됩니다.
   동시 세션은 1개로 제한되며(자격증명 경합 방지), 소켓이 끊기면 컨테이너도 정리됩니다.
3. **Gemini 콜백 릴레이** — 아래 "localhost 리다이렉트" 참고.
4. **패스키 (WebAuthn)** — 암호 외에 패스키로도 로그인할 수 있습니다. 등록은 관리 페이지에서 하고,
   등록된 패스키가 있으면 로그인 화면에 "패스키로 로그인" 버튼이 나타납니다.
   패스키는 **도메인에 고정**되므로 `hunydev-images.exe.xyz` 와 `images.huny.dev` 각각 등록해야 합니다
   (WebAuthn 명세상 자격증명이 RP ID 에 묶입니다). 목록에는 다른 도메인의 키도 보이며 삭제할 수 있습니다.
   discoverable credential(resident key)을 요구하므로 사용자명 입력 없이 바로 인증됩니다.
5. **bake 버튼** — 웹에서 `hunydev/dev:latest` 를 굽고 로그를 실시간으로 스트리밍합니다.

메인 페이지는 발급 **전에** 문제를 알려줍니다. 만료·미로그인 서비스가 있으면 빨간 배너와
관리 페이지 바로가기가 뜨므로, 낡은 이미지를 모르고 새 VM에 배포하는 일을 막습니다.
인증 없이 노출되는 정보는 상태 요약뿐이고, 계정명·파일 경로는 로그인 후에만 보입니다.

## localhost 리다이렉트 회피

원격 VM 에서 OAuth 를 할 때의 핵심 문제입니다. CLI 가 `http://localhost:<포트>` 로 리다이렉트하면
그 주소는 **당신 노트북의 localhost** 로 해석되므로, VM 에서 대기 중인 리스너에 절대 닿지 않습니다.
CLI 별로 대응이 다릅니다:

| CLI | 방식 | localhost 사용? |
| --- | --- | --- |
| `gh auth login` | device code | 안 함 |
| `codex login --device-auth` | device code (`auth.openai.com/codex/device` + 1회용 코드) | 안 함 |
| `claude setup-token` | `platform.claude.com` 로 리다이렉트 후 코드 붙여넣기 | 안 함 |
| `gemini` | **device code 없음** — 항상 임의 포트 localhost 리스너 | **함 → 릴레이 필요** |

앞의 세 개는 그냥 버튼만 누르면 됩니다. `codex login` (플래그 없이) 은 `localhost:1455` 를 쓰므로
쓰지 마세요 — UI 와 CLI 모두 `--device-auth` 를 강제합니다.

Gemini 만 릴레이가 필요합니다. 브라우저에서 승인하면 "연결할 수 없음" 페이지로 떨어지는데,
그 **주소창의 URL 전체**를 복사해서:

- 관리 페이지의 릴레이 입력창에 붙여넣기, 또는
- `hunyimg relay '<URL>'`

VM 안에서 대신 요청하므로 CLI 가 콜백을 받아 로그인이 완료됩니다.
릴레이는 `localhost`/`127.0.0.1` + 비특권 포트만 허용합니다 — 그렇지 않으면 이 엔드포인트가
VM 내부망을 향한 SSRF 도구가 됩니다. 이 거부 규칙은 테스트로 고정돼 있습니다.

## CLI 사용 흐름

```sh
hunyimg build                  # 베이스 이미지 빌드
hunyimg auth gh                # 이미지 안에서 로그인 (또는 hunyimg import)
hunyimg status                 # 자격증명 확인
hunyimg bake                   # hunydev/dev:latest 생성
```

자격증명은 `/var/lib/hunyimg/authhome` 에 보존됩니다. 이미지를 재빌드해도 유지되고, `bake` 할 때만 이미지에 들어갑니다.

### 랩톱에서 이미 로그인돼 있다면

```sh
hunyimg export-hint    # 랩톱에서 실행할 tar 명령을 출력
```

## 명령어

| 명령 | 설명 |
| --- | --- |
| `hunyimg build` | 베이스 이미지 재빌드 |
| `hunyimg auth {gh\|codex\|claude\|claude-token\|gemini\|all}` | 이미지 안에서 로그인 |
| `hunyimg import [file]` | tar 로 자격증명 반입 (기본 stdin) |
| `hunyimg export-hint` | 랩톱에서 쓸 export 명령 출력 |
| `hunyimg shell` | 영속 HOME 을 붙인 대화형 셸 |
| `hunyimg status` | 자격증명 존재 여부 |
| `hunyimg bake` | `hunydev/dev:latest` 생성 |
| `hunyimg verify [img]` | 툴/인증 점검 |
| `hunyimg password` | 웹 비밀번호 변경 |
| `hunyimg token [tok]` | 랩톱 `docker pull` 용 VM 토큰 저장 |
| `hunyimg vend-build` | vend 서비스 재빌드·재시작 |
| `hunyimg grants` | 활성 grant 목록 |
| `hunyimg gc` | registry/도커 가비지 수거 |

## HTTPS 관련 주의

exe.dev 프록시는 HTTPS 만 노출하며 기본이 **인증 게이트(private)** 입니다.

- `ssh exe.dev new --image=…` 는 exe.dev 내부에서 이미지를 당기므로, 프록시가 private 이면 실패합니다.
  `ssh exe.dev share set-public hunydev-images` 로 공개해야 합니다.
- 공개로 두더라도 발급된 토큰 경로를 모르면 아무 것도 볼 수 없습니다. `/v2/` ping 만 `{}` 를 응답하고,
  `/v2/t/<token>/…` 은 유효 토큰·해당 repo·GET/HEAD 로만 제한됩니다.
- 랩톱에서 직접 `docker pull` 하려면 private 상태에서도 VM 토큰을 basic-auth 비밀번호로 쓸 수 있습니다:
  `ssh exe.dev ssh-key generate-api-key --vm=hunydev-images --label=registry` → `hunyimg token <TOKEN>`

## 보안

- 비밀번호는 PBKDF2-HMAC-SHA256 (210k iters, 랜덤 salt) 로 `/etc/hunyimg/config.json` 에 저장
- 5회 실패 후 지수적 lockout (발급/관리 로그인 공유)
- 관리 세션은 HttpOnly·Secure·SameSite=Lax 쿠키, 8시간 만료
- 패스키(WebAuthn)는 암호와 동등한 로그인 수단. RP ID 는 접속 호스트에서 유도하고 origin 을 검증하므로
  다른 도메인의 키로는 인증되지 않습니다. 서명 카운터를 저장해 복제 인증기를 거부합니다.
  패스키 실패도 암호와 같은 lockout 카운터를 공유합니다.
- 터미널·bake·자격증명 상세는 모두 세션 필수 (미인증 시 401)
- 발급 토큰은 128비트 랜덤, 읽기 전용, 단일 repo 스코프, TTL 최대 24h
- backing registry 는 `127.0.0.1:5000` 만 바인딩되어 외부에서 직접 접근 불가

## 파일 위치

- 코드: `/home/exedev/hunyimg`
- 설정: `/etc/hunyimg/config.json`
- 상태: `/var/lib/hunyimg/{grants.json,authhome,registry}`
- 서비스: `hunyimg-vend.service`, `hunyimg-gc.timer`, `registry` (docker)
