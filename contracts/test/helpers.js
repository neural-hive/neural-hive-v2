// Neural Hive test helpers: EIP-712 typed-data digests, secp256k1 signing, devnet agents.
const ethUtil = require('ethereumjs-util');
const crypto = require('crypto');

const TYPES = {
  domain: 'EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)',
  response: 'ResponseAttestation(uint256 requestId,uint256 stepId,address agent,bytes32 outputHash,int256 outputValue,uint256 nonce,uint256 deadline)',
  registration: 'AgentRegistration(address operator,address signer,uint256 nonce,uint256 deadline)',
  trust: 'TrustSignal(address source,address subject,uint256 epoch,uint256 rating)'
};

function h(s) { return web3.utils.keccak256(s); }

function domainSeparator(chainId, verifyingContract) {
  return h(web3.eth.abi.encodeParameters(
    ['bytes32','bytes32','bytes32','uint256','address'],
    [h(TYPES.domain), h('NeuralHive'), h('1'), chainId, verifyingContract]));
}

function responseStructHash(requestId, stepId, agent, outputHashV, outputValue, nonce, deadline) {
  return h(web3.eth.abi.encodeParameters(
    ['bytes32','uint256','uint256','address','bytes32','int256','uint256','uint256'],
    [h(TYPES.response), requestId, stepId, agent, outputHashV, outputValue, nonce, deadline]));
}

function registrationStructHash(operator, signer, nonce, deadline) {
  return h(web3.eth.abi.encodeParameters(
    ['bytes32','address','address','uint256','uint256'],
    [h(TYPES.registration), operator, signer, nonce, deadline]));
}

function digest(domainSep, structHash) {
  return h('0x1901' + domainSep.slice(2) + structHash.slice(2));
}

function ecsign(priv, dgst) {
  const s = ethUtil.ecsign(Buffer.from(dgst.slice(2), 'hex'), Buffer.from(priv.replace('0x', ''), 'hex'));
  const r = s.r.toString('hex').padStart(64, '0');
  const ss = s.s.toString('hex').padStart(64, '0');
  const v = s.v.toString(16).padStart(2, '0');
  return '0x' + r + ss + v;
}

function outputHash(value) {
  return h(web3.eth.abi.encodeParameter('int256', String(value)));
}

function signResponse(priv, chainId, verifyingContract, m) {
  const ds = domainSeparator(chainId, verifyingContract);
  const sh = responseStructHash(m.requestId, m.stepId, m.agent, m.outputHash, m.outputValue, m.nonce, m.deadline);
  return ecsign(priv, digest(ds, sh));
}

function signRegistration(priv, chainId, verifyingContract, operator, signer, nonce, deadline) {
  const ds = domainSeparator(chainId, verifyingContract);
  const sh = registrationStructHash(operator, signer, nonce, deadline);
  return ecsign(priv, digest(ds, sh));
}

function rpc(method, params) {
  var payload = { jsonrpc: '2.0', method: method, params: params, id: Date.now() };
  return new Promise((resolve, reject) => {
    var settled = false;
    var done = (err, res) => { if (settled) { return; } settled = true; if (err) { reject(err); } else { resolve(res); } };
    try {
      var maybe = web3.currentProvider.send(payload, done);
      if (maybe && typeof maybe.then === 'function') { maybe.then(resolve, reject); }
    } catch (e) { reject(e); }
  });
}

// importKey registers a devnet key with the running node so it can send transactions.
// On a real network this is never used: agents hold their own keys and sign locally.
async function importKey(priv) {
  const r = await rpc('personal_importRawKey', [priv, 'neural-hive-devnet']);
  // Ganache v7 imports the key but leaves the account locked, so eth_sendTransaction would
  // fail with 'authentication needed'. Unlock it for the devnet (never done against a real chain).
  await rpc('personal_unlockAccount', [r.result, 'neural-hive-devnet', 0]);
  return r.result;
}

async function newKey() {
  const priv = '0x' + crypto.randomBytes(32).toString('hex');
  const addr = '0x' + ethUtil.privateToAddress(Buffer.from(priv.slice(2), 'hex')).toString('hex');
  const checksum = web3.utils.toChecksumAddress(addr);
  try { web3.eth.accounts.wallet.add(web3.eth.accounts.privateKeyToAccount(priv)); } catch (e) {}
  try { await importKey(priv); } catch (e) {}
  return { priv, addr: checksum };
}

async function fund(from, to, eth) {
  await web3.eth.sendTransaction({ from, to, value: web3.utils.toWei(eth || '1', 'ether') });
}

module.exports = { h, domainSeparator, responseStructHash, registrationStructHash, digest, ecsign, outputHash, signResponse, signRegistration, newKey, fund };
