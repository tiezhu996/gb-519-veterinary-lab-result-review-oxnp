import { useMemo, useState } from 'react';
import type { DomainRecord } from '../types/domain';
import { splitSpecimen } from '../api/specimen';
import { UiButton } from './common/UiButton';

const MIN_PORTIONS = 2;
const MAX_PORTIONS = 5;

interface SplitDialogProps {
  open: boolean;
  mother: DomainRecord | null;
  onClose: () => void;
  onDone: () => void;
}

export function SplitDialog({ open, mother, onClose, onDone }: SplitDialogProps) {
  const [count, setCount] = useState(MIN_PORTIONS);
  const [amounts, setAmounts] = useState<string[]>(['', '', '', '', '']);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const available = Number(mother?.availableAmount ?? 0);
  const unit = mother?.availableUnit || '';
  const total = useMemo(
    () => amounts.slice(0, count).reduce((sum, value) => sum + (Number(value) || 0), 0),
    [amounts, count],
  );
  const remaining = available - total;
  const amountInvalid = useMemo(
    () => amounts.slice(0, count).some((value) => !(Number(value) > 0)),
    [amounts, count],
  );
  const overdrawn = total > available;
  const reasonInvalid = reason.trim().length < 3;
  const canSubmit = !submitting && !amountInvalid && !overdrawn && !reasonInvalid;

  if (!open || !mother) return null;

  const reset = () => {
    setCount(MIN_PORTIONS);
    setAmounts(['', '', '', '', '']);
    setReason('');
    setError('');
    setSubmitting(false);
  };

  const close = () => {
    reset();
    onClose();
  };

  const setAmountAt = (index: number, value: string) => {
    setAmounts((previous) => previous.map((item, itemIndex) => (itemIndex === index ? value : item)));
  };

  const submit = async () => {
    if (!canSubmit || !mother) return;
    setSubmitting(true);
    setError('');
    try {
      await splitSpecimen(mother.id, {
        expectedVersion: mother.version,
        reason: reason.trim(),
        portions: amounts.slice(0, count).map((value) => ({ amount: Number(value) })),
      });
      reset();
      onDone();
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : String(submitError));
      setSubmitting(false);
    }
  };

  return <div className="modal-backdrop">
    <section className="modal modal--wide" role="dialog" aria-modal="true">
      <h2>样本分装 · {mother.code}</h2>
      <p className="split-hint">一次完成母样余量扣减、子样生成与来源记录；余量不足、状态不允许、并发或重复提交时整批拒绝，母样不变。</p>
      <p className="split-meta">
        当前状态 <strong>{mother.status}</strong> · 剩余可用量
        <strong> {available} {unit}</strong>
      </p>
      <label className="split-field">
        <span>分装份数（2-5 份）</span>
        <select value={count} onChange={(event) => setCount(Number(event.target.value))}>
          {[2, 3, 4, 5].map((value) => <option key={value} value={value}>{value} 份</option>)}
        </select>
      </label>
      <div className="split-portions">
        {Array.from({ length: count }, (_, index) => <label key={index} className="split-portion">
          <span>{mother.code}-{String(index + 1).padStart(2, '0')}</span>
          <input
            aria-label={`子样 ${index + 1} 用量`}
            type="number" min="0" step="0.01" placeholder="用量"
            value={amounts[index]} onChange={(event) => setAmountAt(index, event.target.value)}
          />
          <em>{unit}</em>
        </label>)}
      </div>
      <div className={`split-total ${overdrawn ? 'split-total--danger' : ''}`}>
        合计 <strong>{total.toFixed(2)}</strong> {unit} · 分装后余量 <strong>{Math.max(remaining, 0).toFixed(2)}</strong> {unit}
        {overdrawn && <small>余量不足，请减少用量</small>}
      </div>
      <label className="split-field">
        <span>分装原因（至少 3 个字符）</span>
        <input value={reason} maxLength={500} placeholder="例如：PCR 与培养分样" onChange={(event) => setReason(event.target.value)} />
      </label>
      {error && <div className="alert" role="alert">{error}</div>}
      <footer>
        <button className="link-button" onClick={close}>取消</button>
        <UiButton disabled={!canSubmit} onClick={() => void submit()}>{submitting ? '提交中…' : '确认整批分装'}</UiButton>
      </footer>
    </section>
  </div>;
}
