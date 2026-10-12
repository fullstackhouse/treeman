CREATE TABLE dbo.products (id int IDENTITY PRIMARY KEY, name nvarchar(100) NOT NULL);
GO
CREATE TABLE dbo.orders (id int IDENTITY PRIMARY KEY, product_id int NOT NULL REFERENCES dbo.products(id));
GO
INSERT INTO dbo.products(name) VALUES (N'Alpha'), (N'Beta'), (N'zażółć gęślą jaźń');
GO
INSERT INTO dbo.orders(product_id) VALUES (1), (2);
GO
