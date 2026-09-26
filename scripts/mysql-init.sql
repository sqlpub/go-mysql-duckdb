CREATE DATABASE IF NOT EXISTS demo;
USE demo;

CREATE TABLE IF NOT EXISTS users (
  id BIGINT PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  email VARCHAR(128),
  balance DECIMAL(12,2) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS orders (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  amount DECIMAL(12,2) NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'new',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO users (id, name, email, balance) VALUES
  (1, 'alice', 'alice@example.com', 100.00),
  (2, 'bob', 'bob@example.com', 50.50);

INSERT INTO orders (id, user_id, amount, status) VALUES
  (1, 1, 19.99, 'paid'),
  (2, 2, 5.00, 'new');

CREATE USER IF NOT EXISTS 'repl'@'%' IDENTIFIED BY 'replpass';
GRANT SELECT, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'repl'@'%';
GRANT ALL PRIVILEGES ON demo.* TO 'repl'@'%';
FLUSH PRIVILEGES;
