--
-- PostgreSQL database dump
--

\restrict 7qVm2RkXw9Lp4ZcY8sNb3HdJf6GtA1uE5oKiQ0yTr2WxMn9BvC4zLh7PjS3eDgF

-- Dumped from database version 18.1 (Debian 18.1-1.pgdg13+2)
-- Dumped by pg_dump version 18.1 (Debian 18.1-1.pgdg13+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Data for Name: plans; Type: TABLE DATA; Schema: billing; Owner: app
--

INSERT INTO billing.plans (code, "Display Name", price, features, starts_at, note) VALUES ('basic', 'Basic', 10.50, '{wifi,breakfast}', '2026-04-01 00:00:00+00', NULL);
INSERT INTO billing.plans (code, "Display Name", price, features, starts_at, note) VALUES ('premium', 'Premium; with -- and /* inside', 25.00, '{wifi,breakfast,"late checkout"}', '2026-04-01 00:00:00+00', 'first line
second line, it''s here');


--
-- Data for Name: room_types; Type: TABLE DATA; Schema: facility; Owner: app
--

INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (1, 'twin', 'ツイン', 2, true);
INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (2, 'suite', 'スイート', 4, false);
INSERT INTO facility.room_types (id, code, name, capacity, active) VALUES (3, 'dorm', 'Dorm', -1, true);


--
-- Data for Name: rooms; Type: TABLE DATA; Schema: facility; Owner: app
--

INSERT INTO facility.rooms (id, room_type_id, number) OVERRIDING SYSTEM VALUE VALUES (1, 1, '101');
INSERT INTO facility.rooms (id, room_type_id, number) OVERRIDING SYSTEM VALUE VALUES (2, 2, '201');


--
-- Name: room_types_id_seq; Type: SEQUENCE SET; Schema: facility; Owner: app
--

SELECT pg_catalog.setval('facility.room_types_id_seq', 3, true);


--
-- Name: rooms_id_seq; Type: SEQUENCE SET; Schema: facility; Owner: app
--

SELECT pg_catalog.setval('facility.rooms_id_seq', 2, true);


--
-- PostgreSQL database dump complete
--

\unrestrict 7qVm2RkXw9Lp4ZcY8sNb3HdJf6GtA1uE5oKiQ0yTr2WxMn9BvC4zLh7PjS3eDgF

