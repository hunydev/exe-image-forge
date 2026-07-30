# hunyimg

exe.dev 호환 커스텀 이미지를 만들고, **암호 인증 후 일정 시간만 유효한 이미지 경로**를 발급하는 시스템.

## 구성

```
브라우저 ──HTTPS──▶ exe.dev proxy ──▶ :8000 vend (Go)
                                       ├── / , /api/grant   비밀번호 → 임시 토큰 경로 발급
                                       └── /v2/t/<token>/…  토큰 스코프 registry 프록시
                                                  │
                                                  ▼
                                          :5000 registry:2 (localhost only)
```

- `hunydev/base` — node/npm/npx, python, uv/uvx, go, gh, codex, claude, gemini + systemd/exeuntu 호환 레이어
- `hunydev/dev` — base + 로그인된 자격증명을 구운 이미지
- 발급 시 `LABEL`만 다른 얇은 레이어를 새 태그로 push → 태그별 digest가 고유하므로 만료 시 개별 삭제 가능
- 만료되면 태그·토큰 경로 삭제, blob은 매일 `hunyimg-gc.timer`가 회수

## 사용 흐름

```sh
hunyimg build                  # 베이스 이미지 빌드
hunyimg auth gh                # 이미지 안에서 로그인 (또는 hunyimg import)
hunyimg status                 # 자격증명 확인
hunyimg bake                   # hunydev/dev:latest 생성
# 이후 https://hunydev-images.exe.xyz/ 에서 암호 입력 → 경로 발급
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
- 5회 실패 후 지수적 lockout
- 발급 토큰은 128비트 랜덤, 읽기 전용, 단일 repo 스코프, TTL 최대 24h
- backing registry 는 `127.0.0.1:5000` 만 바인딩되어 외부에서 직접 접근 불가

## 파일 위치

- 코드: `/home/exedev/hunyimg`
- 설정: `/etc/hunyimg/config.json`
- 상태: `/var/lib/hunyimg/{grants.json,authhome,registry}`
- 서비스: `hunyimg-vend.service`, `hunyimg-gc.timer`, `registry` (docker)
