# Настройка сервера и клиента

[English](server-setup.md) | [Русский](server-setup.ru.md)

Как поднять VPS-выход и подключиться к нему - как обычным WireGuard-клиентом,
так и через sing-box.

Нужно:

- VPS на Debian или Ubuntu с публичным IPv4-адресом (вне ограниченной сети для
  сценария «выход за границей»);
- для провайдера VK - ссылка на звонок VK (`https://vk.ru/call/join/<hash>`),
  которая остаётся открытой, с подключением с другого устройства, чтобы звонок
  был живым;
- бинарь `turnrelay-udp` (`make build`) и `wireguard-tools` или sing-box на
  клиенте.

## 1. Сервер

Скопируйте `scripts/vps-setup.sh` на VPS и запустите от root:

```bash
scp scripts/vps-setup.sh root@<vps-ip>:/root/
ssh root@<vps-ip> './vps-setup.sh <vps-ip>'
```

Необязательные второй и третий аргументы переопределяют порт WireGuard (по
умолчанию 51820) и порт прокси (по умолчанию 56004):

```bash
./vps-setup.sh <vps-ip> 51820 56004
```

Скрипт устанавливает WireGuard и серверную часть (собранную из upstream-сервера
`vk-turn-proxy` на зафиксированном коммите) и идемпотентен - повторный запуск
не перегенерирует ключи и не клонирует заново. Он открывает порт прокси (56004)
в интернет, куда подключается релей, и держит порт WireGuard только на loopback
(публичный порт WireGuard легко детектится сканерами и сводит на нет весь
смысл). Скрипт выводит два ключа WireGuard и адрес для клиента:

```
server public key:   <...>
client private key:  <...>
proxy endpoint:      <vps-ip>:56004
wg client address:   10.8.0.2/32
wg server address:   10.8.0.1
```

Сохраните этот вывод.

## 2. Клиентский мост: turnrelay-udp

`turnrelay-udp` открывает TURN-аллокации и представляет их как локальный
UDP-сокет. Запустите на клиенте:

```bash
turnrelay-udp -listen 127.0.0.1:9000 -provider vk \
  -link https://vk.ru/call/join/<hash> \
  -server <vps-ip>:56004 -n 18 -mode srtp
```

Дождитесь, пока поднимется воркер. Каждая аллокация пишет в лог:

```
mux: worker 0 up via <relay-host>:<relay-port> relayed <alloc-addr>
```

`<relay-host>` - это реальный релей в работе; запишите его, конфиг WireGuard
ниже должен маршрутизировать его мимо туннеля. Следите за периодической
строкой `stats:`, чтобы увидеть, как поднимаются все воркеры (`Active:`
достигает `-n`). Капча VK, если есть, решается автоматически.

## 3. Клиент: WireGuard

Направьте WireGuard-клиент на локальный мост. Создайте `wg.conf`:

```ini
[Interface]
PrivateKey = <client private key из шага 1>
Address = 10.8.0.2/32
DNS = 1.1.1.1
MTU = 1280

[Peer]
PublicKey = <server public key из шага 1>
Endpoint = 127.0.0.1:9000
AllowedIPs = ...
PersistentKeepalive = 5
```

`Endpoint` - это `127.0.0.1:9000` (локальный мост), а не VPS. **MTU должен
быть 1280**: полезная нагрузка уже несёт оверхед фреймов релея и обфускации,
а больший MTU приводит к фрагментации или чёрным дырам через релей.

### AllowedIPs: держите релей вне туннеля

Наивный `AllowedIPs = 0.0.0.0/0` поглощает и маршрут к TURN-релею, который
`turnrelay-udp` должен достигать напрямую. Исключите диапазон релея. Два
способа:

**A. Два блока /1 плюс закреплённый маршрут (проще всего на десктопе):**

```ini
AllowedIPs = 0.0.0.0/1, 128.0.0.0/1
PostUp = ip route add <relay-range> via <gateway> dev <iface>
PostDown = ip route del <relay-range> via <gateway> dev <iface>
```

`<gateway>` и `<iface>` найдите через `ip route show default`.
`<relay-range>` - сеть релея из строки лога `via <relay>` (у VK это примерно
`155.212.192.0/20` и `193.203.43.0/24`; смотрите в логе свой).

**B. Калькулятор AllowedIPs (переносимо, например для мобильных):**
используйте
<https://www.procustodibus.com/blog/2021/03/wireguard-allowedips-calculator/>,
чтобы вычесть диапазон релея из `0.0.0.0/0`, и вставьте полученный список CIDR
в `AllowedIPs`. `PostUp` не нужен.

Поднимайте, когда воркер уже жив:

```bash
wg-quick up ./wg.conf
curl https://ifconfig.me   # должен вернуть IP вашего VPS
```

## 4. Клиент: sing-box (маршрутизация)

Чтобы маршрутизировать по правилам, а не гнать всё в туннель, используйте
sing-box с endpoint `wireguard`, чей пир - локальный мост. Минимальный конфиг:

```json
{
  "inbounds": [
    { "type": "mixed", "listen": "127.0.0.1", "listen_port": 1080 }
  ],
  "endpoints": [
    { "type": "wireguard", "tag": "wg",
      "address": ["10.8.0.2/32"], "mtu": 1280,
      "private_key": "<client private key>",
      "peers": [ { "address": "127.0.0.1", "port": 9000,
                   "public_key": "<server public key>",
                   "allowed_ips": ["0.0.0.0/0"],
                   "persistent_keepalive_interval": 15 } ] }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": {
    "rules": [
      { "rule_set": "geosite-ru", "outbound": "direct" },
      { "rule_set": "geoip-ru",   "outbound": "direct" }
    ],
    "final": "wg"
  }
}
```

Здесь `allowed_ips: 0.0.0.0/0` - не маршрутизация: что попадает в endpoint
`wg`, решают правила sing-box; всё, что ушло `direct`, туннеля не касается.
Если ограниченная сеть ломает ещё и DNS, направьте DNS sing-box через endpoint
(`"dns": {"servers": [{"type": "udp", "server": "8.8.8.8", "detour": "wg"}]}`).

## Диагностика

- **`curl` возвращает ваш IP, а не VPS:** общий трафик не входит в туннель.
  Проверьте исключение `AllowedIPs` / маршрут на двух /1 и `wg show` для
  allowed IPs пира.

- **Рукопожатие WireGuard не завершается:** проверьте, что `-server
  <vps-ip>:56004` совпадает с портом прокси, что firewall VPS пропускает
  входящий UDP на него и что хотя бы одна строка `worker N up` появилась до
  `wg-quick up`. Порт WireGuard (51820) по замыслу только на loopback - не
  открывайте его.

- **Туннель поднялся, но трафик стоит или фрагментируется:** проверьте
  `MTU = 1280`.

- **Ошибка TURN 486 (Allocation Quota Reached):** нормально после ~18-20
  аллокаций на одном credential; пул переводит воркер на свежий credential.
  Постоянный 486 по всем слотам сразу после перезапуска означает, что
  аллокации прошлого запуска ещё считаются на релее (истекают за несколько
  минут); подождите и повторите.

- **Сообщается о капче (`CaptchaUntil:` в будущем):** автосолвер не прошёл.
  Перезапустите с меньшим числом соединений и дождитесь кулдауна; не
  долбите ретраями, это его продлевает.

- **Выгрузка сильно ниже загрузки:** страйпленный uplink приходит с
  переупорядочиванием, и TCP внутри туннеля читает это как потери. Запустите
  сервер с `-uplink-reseq 100ms` (скрипт настройки это делает).

- **Docker на VPS:** Docker выставляет политику цепочки `FORWARD` в DROP, так
  что WireGuard-клиенты делают рукопожатие, но трафика нет. Скрипт настройки
  добавляет правила `FORWARD` accept для `wg0`; добавьте их вручную, если ваш
  сервер старше.

## Сервер-выход (без WireGuard)

`turnrelay-server` сам терминирует транспорт и подключается к адресам
назначения, так что VPS не нужен WireGuard, а клиенту не нужен endpoint
`wireguard`. Требуется общий пароль: сервер, подключающийся к произвольным
адресам без пароля, - это открытый прокси. По умолчанию он отказывает в
приватных, loopback и link-local адресах назначения (измените это через
`-allow-private`).

```bash
sudo PASSWORD='choose-a-long-random-password' bash scripts/vps-setup-exit.sh
```

Флаги (`turnrelay-server -h`): `-listen` (по умолчанию `:56004`), `-mode`
(`srtp` по умолчанию, `wrap`, `dtls`), `-password` или `TURNRELAY_PASSWORD`,
`-bind`, `-allow-private`, `-dial-timeout`, `-max-streams`, `-udp-timeout`.

Клиенты: в sing-box установите `"server_type": "exit"` и `"password"` в
outbound `turnrelay` и уберите endpoint `wireguard` - outbound станет обычным
TCP+UDP proxy outbound. Без sing-box запустите локальный SOCKS5-прокси:

```bash
turnrelay-proxy -server <vps-ip>:56004 -password '...' -links https://vk.ru/call/join/<hash>
```

и направьте любой SOCKS5-клиент на `127.0.0.1:1080`.
