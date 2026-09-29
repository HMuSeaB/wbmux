package credential

// helperJS 是在客户端运行时里执行的一小段脚本，只做一件事：
// 把喂进来的信封对象解开，把明文令牌按 JSON 回话吐回 stdout。
//
// # 为什么用内联脚本，而不是命令行参数传路径
//
// 载荷必须是在这里写死的常量。若改成"把信封写进临时文件、命令行传路径"，
// 就多了一个可被替换的输入面（临时文件可被同机其它进程改写），
// 等于给客户端进程塞了一个任意执行入口。现在这条路径上唯一的输入是
// stdin 里的一段 JSON，且只被当作数据解析。
//
// # 为什么不用 Go 实现
//
// 密钥由客户端的原生模块（electron_browser_workbuddy_storage）持有，
// 只在它自己的进程里拿得到。Go 侧能做的只有"请它替我们解一次"。
// 顺带的好处是：整套密码学都不需要 Go 依赖，零依赖的红线保住了。
//
// # 协议
//
//	stdin  : {"version":1,"operation":"decrypt","value":<信封对象>}
//	stdout : {"ok":true,"accessToken":"<明文>"}
//	       | {"ok":false,"reason":"<机器码，见 helperReasonText>"}
//
// 失败一律以**固定机器码**回报，绝不把原始异常或载荷回显出去——
// stdout/stderr 是要被 Go 侧读走并可能进日志的，不能夹带令牌。
//
// 算法细节（与 88lin/workbuddy-auto-signin 的实现一致，本机实测可解）：
// 密钥 = SHA-256(atRestSecretKey 这个 **base64 字符串本身** 的 UTF-8 字节)，
// 不是解码后的 32 字节——这里写错会得到 DECRYPT_FAILED 且看不出原因。
// 信封为 AES-256-GCM，nonce 12 字节、authTag 16 字节，
// AAD = "WB-AAD\0" + [1] + lp("WBEV1") + lp("sym-v1") + [0,0,0,1] + lp(keyId) + [2,0,0]。
const helperJS = `
'use strict';
const crypto = require('crypto');
const failure = r => { throw { reason: r }; };
const object = x => x !== null && typeof x === 'object' && !Array.isArray(x);

let chunks = [];
let size = 0;

function reply(v) {
  process.stdout.write(JSON.stringify(v));
  process.exit(v.ok ? 0 : 1);
}

function b64(value, length) {
  if (typeof value !== 'string' || value.length > 65536) failure('INVALID_FORMAT');
  const bytes = Buffer.from(value, 'base64');
  if (bytes.toString('base64') !== value) failure('INVALID_FORMAT');
  if (length !== undefined && bytes.length !== length) failure('INVALID_FORMAT');
  return bytes;
}

function utf8(bytes) {
  const text = bytes.toString('utf8');
  if (!Buffer.from(text, 'utf8').equals(bytes)) failure('INVALID_FORMAT');
  return text;
}

function decodeEnvelope(value) {
  if (!object(value)) failure('INVALID_FORMAT');
  if (Object.keys(value).sort().join(',') !== '$wbEncrypted,envelope') failure('INVALID_FORMAT');
  if (value.$wbEncrypted !== 1) failure('UNSUPPORTED_ENVELOPE');

  let envelope;
  try {
    envelope = JSON.parse(utf8(b64(value.envelope)));
  } catch (e) {
    failure(e && e.reason ? e.reason : 'INVALID_FORMAT');
  }
  if (!object(envelope) || !Number.isInteger(envelope.suite)) failure('INVALID_FORMAT');
  if (envelope.suite !== 1) failure('UNSUPPORTED_ENVELOPE');
  if (Object.keys(envelope).sort().join(',') !== 'authTag,ciphertext,keyId,nonce,suite') failure('INVALID_FORMAT');
  if (typeof envelope.keyId !== 'string' || !/^[0-9a-f]{16}$/.test(envelope.keyId)) failure('INVALID_FORMAT');

  return {
    keyId: envelope.keyId,
    nonce: b64(envelope.nonce, 12),
    tag: b64(envelope.authTag, 16),
    ciphertext: b64(envelope.ciphertext)
  };
}

function nativeStorage() {
  try {
    const s = process._linkedBinding('electron_browser_workbuddy_storage');
    if (typeof s.loggerGet !== 'function') failure('RUNTIME_UNAVAILABLE');
    return s;
  } catch (_) {
    failure('RUNTIME_UNAVAILABLE');
  }
}

function decrypt(envelope) {
  let payload;
  try {
    payload = JSON.parse(nativeStorage().loggerGet());
  } catch (_) {
    failure('RUNTIME_UNAVAILABLE');
  }
  if (!object(payload) || payload.version !== 1) failure('RUNTIME_UNAVAILABLE');

  let secret;
  try {
    secret = b64(payload.atRestSecretKey, 32);
  } catch (_) {
    failure('RUNTIME_UNAVAILABLE');
  }
  if (secret.every(b => b === 0)) failure('RUNTIME_UNAVAILABLE');
  secret.fill(0);

  const key = crypto.createHash('sha256').update(payload.atRestSecretKey, 'utf8').digest();
  payload = null;

  try {
    if (crypto.createHash('sha256').update(key).digest('hex').slice(0, 16) !== envelope.keyId) {
      failure('KEY_MISMATCH');
    }
    const lp = s => {
      const b = Buffer.from(s, 'utf8');
      const l = Buffer.alloc(4);
      l.writeUInt32BE(b.length);
      return Buffer.concat([l, b]);
    };
    const aad = Buffer.concat([
      Buffer.from('WB-AAD\0', 'ascii'),
      Buffer.from([1]),
      lp('WBEV1'),
      lp('sym-v1'),
      Buffer.from([0, 0, 0, 1]),
      lp(envelope.keyId),
      Buffer.from([2, 0, 0])
    ]);

    let plaintext;
    try {
      const c = crypto.createDecipheriv('aes-256-gcm', key, envelope.nonce, { authTagLength: 16 });
      c.setAAD(aad);
      c.setAuthTag(envelope.tag);
      plaintext = Buffer.concat([c.update(envelope.ciphertext), c.final()]);
    } catch (_) {
      failure('DECRYPT_FAILED');
    }

    const token = utf8(plaintext);
    plaintext.fill(0);
    if (token.length === 0 || token.length > 32768) failure('INVALID_FORMAT');
    return token;
  } finally {
    key.fill(0);
  }
}

process.stdin.on('data', c => {
  size += c.length;
  if (size > 65536) {
    reply({ ok: false, reason: 'INVALID_FORMAT' });
    return;
  }
  chunks.push(c);
});

process.stdin.on('end', () => {
  try {
    const req = JSON.parse(utf8(Buffer.concat(chunks)));
    if (!object(req) || req.version !== 1 || req.operation !== 'decrypt' || !('value' in req)) {
      failure('HELPER_PROTOCOL');
    }
    reply({ ok: true, accessToken: decrypt(decodeEnvelope(req.value)) });
  } catch (e) {
    reply({ ok: false, reason: (e && e.reason) ? e.reason : 'HELPER_PROTOCOL' });
  }
});

process.stdin.resume();
`
