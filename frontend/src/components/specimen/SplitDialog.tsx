import { useEffect, useMemo, useState } from 'react';
import type { DomainRecord } from '../../types/domain';

interface SplitDialogProps {
  item: DomainRecord;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (parts: number[], reason: string, clientToken: string) => Promise<void> | void;
}

function newToken(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return `split-${crypto.randomUUID()}`;
  }
  return `split-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`;
}

const MIN_PARTS = 2;
const MAX_PARTS = 5;

export function SplitDialog({ item, busy, onCancel, onConfirm }: SplitDialogProps) {
  const [partCount, setPartCount] = useState(MIN_PARTS);
  const [values, setValues] = useState<string[]>(() => Array(MIN_PARTS).fill(''));
  const [reason, setReason] = useState('');
  const [token] = useState(newToken);
  const [formError, setFormError] = useState('');

  const unit = item.quantityUnit || '';
  const available = item.availableQuantity ?? 0;
  // 新批次子样序号在同一母样既有最大序号之后连续顺延。
  const existing = item.children?.length ?? 0;

  useEffect(() => {
    setValues((previous) => {
      const next = previous.slice(0, partCount);
      while (next.length < partCount) next.push('');
      return next;
    });
  }, [partCount]);

  const parts = useMemo(() => values.map((value) => Number.parseFloat(value)), [values]);
  const total = parts.reduce((sum, value) => sum + (Number.isFinite(value) ? value : 0), 0);
  const validParts = parts.every((value) => Number.isFinite(value) && value > 0);
  const totalExceeds = total > available + 1e-9;
  const reasonValid = reason.trim().length >= 3;
  const canSubmit = validParts && !totalExceeds && reasonValid && !busy;

  const submit = async () => {
    if (!canSubmit) {
      setFormError(totalExceeds ? '分配总量超过母样剩余可用量' : '请检查每份分配量与分装原因');
      return;
    }
    await onConfirm(parts.map((value) => Math.round(value * 10000) / 10000), reason.trim(), token);
  };

  return <div className="modal-backdrop">
    <section className="modal modal--wide" role="dialog" aria-modal="true">
      <h2>样本分装 · {item.code}</h2>
      <p className="split-summary">剩余可用量 <strong>{available} {unit}</strong>，当前版本 v{item.version}。一次提交扣减母样余量并生成 {MIN_PARTS}-{MAX_PARTS} 份连续编号子样。</p>
      <label className="split-field">
        <span>分装份数</span>
        <select value={partCount} onChange={(event) => setPartCount(Number(event.target.value))} disabled={busy}>
          {Array.from({ length: MAX_PARTS - MIN_PARTS + 1 }, (_, index) => MIN_PARTS + index).map((count) =>
            <option key={count} value={count}>{count} 份</option>)}
        </select>
      </label>
      <div className="split-parts">
        {values.map((value, index) => {
          const sequence = existing + index + 1;
          return <label key={index} className="split-field">
            <span>子样 {String(sequence).padStart(2, '0')}（{item.code}-C{String(sequence).padStart(2, '0')}）</span>
            <input type="number" min="0" step="0.0001" placeholder={`分配量${unit ? ` (${unit})` : ''}`} value={value}
              disabled={busy}
              onChange={(event) => setValues((previous) => previous.map((entry, entryIndex) => entryIndex === index ? event.target.value : entry))} />
          </label>;
        })}
      </div>
      <div className={`split-total ${totalExceeds ? 'split-total--over' : ''}`}>
        合计 {Math.round(total * 10000) / 10000} {unit} / 可用 {available} {unit}
      </div>
      <label className="split-field">
        <span>分装原因</span>
        <input type="text" maxLength={500} placeholder="至少 3 个字符，将写入审计日志" value={reason} disabled={busy}
          onChange={(event) => setReason(event.target.value)} />
      </label>
      {formError && <div className="alert" role="alert">{formError}</div>}
      <p className="split-token">批次令牌：<code>{token}</code>（重复提交自动整批去重）</p>
      <footer>
        <button className="link-button" onClick={onCancel} disabled={busy}>取消</button>
        <button className="table-action" disabled={!canSubmit} onClick={() => void submit()}>确认分装</button>
      </footer>
    </section>
  </div>;
}
