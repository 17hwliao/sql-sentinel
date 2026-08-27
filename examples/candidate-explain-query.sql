SELECT id, amount_cents
FROM orders
WHERE status = 'paid'
ORDER BY amount_cents DESC
LIMIT 20;
