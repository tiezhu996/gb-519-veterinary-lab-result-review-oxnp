
import { request } from './client';
import type { DomainRecord, SpecimenChild } from '../types/domain';

export interface SplitSpecimenPayload {
  expectedVersion: number;
  parts: number[];
  reason: string;
  clientToken: string;
}

export interface SplitSpecimenResponse {
  parent: DomainRecord;
  children: DomainRecord[];
  count: number;
}

export async function listSpecimen(page = 1, pageSize = 20, search = '') {
  return request<DomainRecord[]>(`/specimens?page=${page}&pageSize=${pageSize}&search=${encodeURIComponent(search)}`);
}
export async function createSpecimen(input: Partial<DomainRecord> & { quantity?: number }) {
  return request<DomainRecord>('/specimens', { method: 'POST', body: JSON.stringify(input) });
}
export async function transitionSpecimen(id: number, status: string, expectedVersion: number, reason: string) {
  return request<DomainRecord>(`/specimens/${id}/transition`, {
    method: 'POST', body: JSON.stringify({ status, expectedVersion, reason }),
  });
}
export async function splitSpecimen(id: number, payload: SplitSpecimenPayload) {
  return request<SplitSpecimenResponse>(`/specimens/${id}/splits`, {
    method: 'POST', body: JSON.stringify(payload),
  });
}

export type { SpecimenChild };
